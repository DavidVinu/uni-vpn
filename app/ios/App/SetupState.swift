import Foundation
import Observation

/// One university of /universities.json.
struct University: Identifiable {
    let json: JSON
    var id: String { json.string("id") }
    var name: String { json.string("name") }
    var country: String { json.string("country") }
    var mfa: String { json.string("mfa") }
    var portal: String { json.string("mfa_portal_url") }
    var steps: [String] { json.strings("mfa_steps") }
    var enroll: String { json.string("mfa_enroll") }
    var search: [String] { [id, name, json.string("host")] + json.strings("aliases") }
}

/// What setup has entered so far and where it is, held by the AppModel so it survives the views.
/// The same steps and rules as the Android app's SetupScreen.kt.
@MainActor
@Observable
final class SetupState {
    enum Step: Int {
        case welcome = -1, code = -2, university = 0, signIn = 1, secondFactor = 2, connecting = 3, done = 4
    }

    static let totpModes: Set<String> = ["totp_field", "totp_append"]
    static let profileFields: Set<String> = ["university", "host", "usergroup", "authgroup", "mfa"]

    private(set) var prepared = false
    var step = Step.university
    var universities: [University] = []
    var uniId = ""
    var query = ""
    var host = ""
    var groups: [String] = []
    var group = ""
    /// The account's own group (an account change): it shows, also as the only choice, until the
    /// gateway is asked again.
    private(set) var keptGroup = false
    var otherMfa = ""
    var detecting = false
    var user = ""
    var password = ""
    var secret = ""
    var secretValid = false
    /// The second factor with the key after all, where the one code (AutoEnroll) is offered.
    var byHand = false
    var pairCode = ""
    var error: String?
    var busy = false
    /// The setup request is out.
    private(set) var sending = false
    /// When setup was accepted and the first sign-in started; nil while none is awaited.
    var sentAt: Date?

    /// For the bindings below; set by prepare.
    @ObservationIgnored private weak var model: AppModel?

    var uni: University? { universities.first { $0.id == uniId } }

    /// The search field: shows the chosen university, typing starts a new search.
    var queryText: String {
        get {
            if let uni, query.isEmpty { return uni.name }
            if isOther && query.isEmpty { return model?.t("setup.not_listed") ?? "" }
            return query
        }
        set {
            query = newValue
            uniId = ""
        }
    }

    /// A new VPN address asks the gateway again.
    var hostText: String {
        get { host }
        set {
            host = newValue
            otherMfa = ""
            groups = []
            keptGroup = false
        }
    }

    /// Another group may ask for another second factor: ask again.
    var pickedGroup: String {
        get { group }
        set {
            group = newValue
            guard let model else { return }
            Task { await detect(model, group: newValue) }
        }
    }
    var isOther: Bool { uniId == "other" }
    var showsGroups: Bool { groups.count > 1 || keptGroup }
    var mfa: String { isOther ? otherMfa : (uni?.mfa ?? "totp_field") }
    var needsTotp: Bool { SetupState.totpModes.contains(mfa) }
    /// One code from the user's app sets up the second factor (the desktop's "enroll").
    var offersAuto: Bool { uni?.enroll == "linotp" }
    var total: Int { needsTotp ? 4 : 3 }

    /// Where setup starts, once per run: Settings changes the account, a phone that can sync
    /// first offers to take the settings from another device.
    func prepare(_ model: AppModel) {
        guard !prepared, let status = model.status else { return }
        prepared = true
        self.model = model
        let syncAvailable = status.object("sync")?.bool("available") == true
        if model.setupFinishing {
            step = .done
        } else if model.changingAccount || !syncAvailable {
            step = .university
        } else {
            step = .welcome
        }
        // The university once the list is there (loadUniversities).
        if model.changingAccount { user = status.string("user") }
    }

    /// The list, again until it comes: a core that is still starting or away for a moment must
    /// not leave setup with "Not listed" alone. Runs while setup shows (SetupView's task).
    func loadUniversities(_ model: AppModel) async {
        while universities.isEmpty && !Task.isCancelled {
            let list = await model.get("/universities.json")?.objects("universities") ?? []
            if list.isEmpty {
                try? await Task.sleep(nanoseconds: 1_000_000_000)
                continue
            }
            universities = list.map(University.init)
            // Only now: without the list every account would look "Not listed" (the desktop's
            // startChange).
            if model.changingAccount, let status = model.status { takeAccount(status) }
        }
    }

    /// An account change starts from the account as it is. Not listed: with its gateway settings
    /// too, or Next would set the account up without them.
    func takeAccount(_ status: JSON) {
        let current = status.string("university")
        if universities.contains(where: { $0.id == current }) {
            uniId = current
            return
        }
        uniId = "other"
        let usergroup = status.string("usergroup")
        host = status.string("host") + (usergroup.isEmpty ? "" : "/" + usergroup)
        let factor = status.string("mfa")
        otherMfa = factor.isEmpty ? "none" : factor
        group = status.string("authgroup")
        groups = group.isEmpty ? [] : [group]
        keptGroup = !group.isEmpty
    }

    func go(_ next: Step) {
        error = nil
        step = next
    }

    func submit(_ model: AppModel) {
        if sending { return }
        model.setupFinishing = true
        go(.connecting)
        let body = request(secret: needsTotp ? secret.trimmingCharacters(in: .whitespacesAndNewlines) : "")
        sending = true
        sentAt = nil
        Task {
            let result = await model.post("/api/setup", body, timeout: 60)
            sending = false
            if result.ok {
                sentAt = Date()
            } else {
                failed(model, result)
            }
        }
    }

    /// What /api/setup gets: the account and the key, or a code from the user's app instead
    /// (AutoEnroll).
    func request(secret: String, code: String = "") -> [String: Any] {
        var profile: [String: Any] = [:]
        if isOther {
            var address = host.trimmingCharacters(in: .whitespacesAndNewlines)
            for scheme in ["https://", "http://"] where address.lowercased().hasPrefix(scheme) {
                address = String(address.dropFirst(scheme.count))
            }
            let parts = address.split(separator: "/", maxSplits: 1, omittingEmptySubsequences: false).map(String.init)
            profile["host"] = parts[0]
            profile["mfa"] = otherMfa
            if parts.count > 1 && !parts[1].isEmpty { profile["usergroup"] = parts[1] }
            if showsGroups && !group.isEmpty { profile["authgroup"] = group }
        }
        var body: [String: Any] = [
            "user": user.trimmingCharacters(in: .whitespacesAndNewlines),
            "password": password,
            "secret": secret,
            "university": uniId,
            "profile": profile,
        ]
        if !code.isEmpty { body["code"] = code }
        return body
    }

    func failed(_ model: AppModel, _ result: PostResult) {
        // An account sent before (its sign-in failed, or left with the back arrow) keeps the
        // setup page, which says why this one was not taken: no Home with the old account.
        if !model.changingAccount && model.setupNeeded { model.setupFinishing = false }
        let field = result.json?.string("field") ?? ""
        if SetupState.profileFields.contains(field) {
            step = .university
        } else if field == "totp" && needsTotp {
            step = .secondFactor
        } else {
            step = .signIn
        }
        error = result.error
    }

    /// While connecting: the first sign-in tells whether everything was right.
    func statusChanged(_ model: AppModel) {
        guard step == .connecting, let sentAt, let status = model.status else { return }
        let state = status.string("state")
        let settled = Date().timeIntervalSince(sentAt) > 1.5
        if state == "connected" {
            self.sentAt = nil
            if model.changingAccount { model.finishSetup() } else { go(.done) }
        } else if ["auth_failed", "keyring"].contains(state) && settled {
            self.sentAt = nil
            let totp = status.string("error_kind") == "totp"
            step = totp && needsTotp ? .secondFactor : .signIn
            error = model.strings.message(status.string("message"), status.object("message_t"))
        } else if state == "idle" && !status.bool("busy") && settled {
            self.sentAt = Date()
            Task { _ = await model.post("/api/connect") }
        }
    }

    /// A pairing code another device showed (scanned or typed).
    func enterCode(_ model: AppModel) async {
        busy = true
        error = nil
        // Setup stays for its last page ("All set"), also while the core already counts as set
        // up and its answer is still on the way: no Home in between (Android's enterPairCode).
        let finishing = model.setupFinishing
        let wasNeeded = model.setupNeeded
        model.setupFinishing = true
        let result = await model.post("/api/sync/enter", ["code": pairCode], timeout: 60)
        busy = false
        // Linked, but some settings could not be used here: the core says so in a note.
        let note: String? = result.json.map { model.strings.message($0.string("error"), $0.object("error_t")) }
            .flatMap { $0.isEmpty ? nil : $0 }
        if !result.ok && !(wasNeeded && !model.setupNeeded) {
            // Not linked. An account sent before (and left with the back arrow) keeps the setup
            // page, which says why the code did not work.
            model.setupFinishing = finishing && !model.setupNeeded
            error = result.error
        } else if model.setupNeeded {
            model.setupFinishing = false
            // The account did not come over: setup goes on by hand with that note, not with an
            // "All set" that would bring it back.
            go(.university)
            error = note
        } else {
            go(.done)
            // Also when the answer failed after the account came over (the network went away
            // before the other device heard back): set up all the same, with the core's error as
            // the note; none when only the wait for the answer ran out ("not running" is wrong).
            error = result.ok ? note : (result.reply.status == 0 ? nil : result.error)
        }
    }

    /// Asks the gateway of a VPN address that is not listed what it needs.
    func detect(_ model: AppModel, group picked: String) async {
        detecting = true
        error = nil
        let result = await model.post("/api/detect", ["host": host.trimmingCharacters(in: .whitespacesAndNewlines), "group": picked], timeout: 30)
        detecting = false
        guard result.ok, let d = result.json else {
            error = result.error
            return
        }
        // The core always sends "error", empty when the gateway answered (as the page reads it).
        if !d.string("error").isEmpty {
            error = model.strings.message(d.string("error"), d.object("error_t"))
            return
        }
        if d.bool("saml") {
            error = model.t("setup.saml")
            return
        }
        keptGroup = false
        group = d.string("group")
        groups = d.strings("groups")
        let suggested = d.object("suggestion")?.string("mfa") ?? ""
        otherMfa = suggested.isEmpty ? "totp_field" : suggested
    }

    /// A university's own steps, translated where the catalog has them, else the general ones.
    func mfaSteps(_ model: AppModel) -> [String] {
        let portal = model.t("setup.mfa_portal")
        guard let uni, !uni.steps.isEmpty else {
            return (1...3).map { "\($0). " + model.t("setup.generic_step.\($0)", ["portal": portal]) }
        }
        return uni.steps.enumerated().map { i, text in
            let key = "uni.\(uni.id).mfa.\(i + 1)"
            let translated = model.t(key)
            let line = translated == key ? text : translated
            return "\(i + 1). " + line.replacingOccurrences(of: "{portal}", with: portal)
        }
    }

    /// Like the status page's search: no accents, umlaut spellings folded.
    static func fold(_ s: String) -> String {
        s.folding(options: [.diacriticInsensitive, .caseInsensitive], locale: nil).lowercased()
            .replacingOccurrences(of: "ue", with: "u")
            .replacingOccurrences(of: "oe", with: "o")
            .replacingOccurrences(of: "ae", with: "a")
    }

    func found() -> [University] {
        let q = SetupState.fold(query.trimmingCharacters(in: .whitespacesAndNewlines))
        let matches = universities.filter { u in q.isEmpty || u.search.contains { SetupState.fold($0).contains(q) } }
        return Array(matches.prefix(8))
    }
}

/// The second factor the short way, as on the desktop page (step2a): the user types the code
/// their authenticator app shows, the core signs in with it and adds itself in the MFA portal
/// as a code generator, asking for the next code if the portal wants one (status.json
/// "enroll"). Held by the AppModel, as the run goes on while the user reads the next code in
/// the other app.
@MainActor
@Observable
final class AutoEnroll {
    static let runningStages: Set<String> = ["connecting", "code", "working"]

    var code = ""
    var error: String?
    /// A run was started here and is followed until it is done or failed.
    private(set) var running = false
    /// The request is out.
    private(set) var sending = false
    /// The run belongs to setup, else to Settings, second factor.
    private(set) var inSetup = false
    private var sentAt = Date.distantPast
    /// What the request just sent started, until the status catches up.
    private var pending = ""
    @ObservationIgnored private var cancelledAt = Date.distantPast

    func stage(_ status: JSON?) -> String {
        guard running else { return "" }
        if Date().timeIntervalSince(sentAt) < 1.5 { return pending }
        return status?.object("enroll")?.string("stage") ?? ""
    }

    /// Nothing to type: the request is out or the core is busy.
    func busy(_ status: JSON?) -> Bool {
        let now = stage(status)
        return sending || now == "connecting" || now == "working"
    }

    /// The line above the field, as on the desktop page.
    func text(_ model: AppModel) -> String {
        let status = model.status ?? JSON([:])
        let now = stage(status)
        if now == "code", let e = status.object("enroll") {
            return model.strings.message(e.string("error"), e.object("error_t"))
        }
        if now == "connecting" && ["offline", "blocked", "error"].contains(status.string("state")) {
            return model.strings.message(status.string("message"), status.object("message_t"))
        }
        if busy(status) { return model.t(now == "working" ? "setup.auto_working" : "setup.waiting") }
        return model.t("setup.auto_sub")
    }

    /// Next: a code the core asks for answers the run; else the first code starts one, with the
    /// account in setup (also when the account changes), alone from Settings.
    func send(_ model: AppModel, setup: SetupState?) async {
        if sending { return }
        let typed = code.filter { !$0.isWhitespace }
        guard (6...8).contains(typed.count), typed.allSatisfy({ $0.isASCII && $0.isNumber }) else {
            error = model.t("enroll.enter_code")
            return
        }
        let answer = stage(model.status) == "code"
        sending = true
        error = nil
        let result: PostResult
        if let setup, !answer {
            model.setupFinishing = true
            result = await model.post("/api/setup", setup.request(secret: "", code: typed), timeout: 60)
        } else {
            result = await model.post("/api/totp/auto", ["code": typed])
        }
        sending = false
        guard result.ok else {
            // A wrong university, username or password: back to its step.
            let field = result.json?.string("field") ?? ""
            if let setup, !answer, !field.isEmpty, field != "totp" {
                setup.failed(model, result)
                return
            }
            error = result.error
            return
        }
        running = true
        inSetup = setup != nil
        sentAt = Date()
        pending = answer || (setup == nil && model.status?.string("state") == "connected") ? "working" : "connecting"
        code = ""
    }

    /// Back or by hand: the core stops the run.
    func cancel() {
        code = ""
        error = nil
        guard running else { return }
        running = false
        cancelledAt = Date()
        Task { _ = await API.post("/api/totp/auto", ["cancel": true]) }
    }

    /// Every status: the end of the run, or a run the app does not follow yet.
    func progress(_ model: AppModel) {
        guard let status = model.status else { return }
        let e = status.object("enroll")
        let now = e?.string("stage") ?? ""
        if !running {
            // Opened again while a run goes on (in the VPN's core): back to its page.
            guard !sending, AutoEnroll.runningStages.contains(now),
                  Date().timeIntervalSince(cancelledAt) > 3 else { return }
            running = true
            sentAt = .distantPast
            inSetup = model.setupNeeded || model.changingAccount || model.setupFinishing
            if inSetup {
                model.setup.byHand = false
                model.setup.go(.secondFactor)
            } else if model.path.last != .secondFactor {
                model.path = [.settings, .secondFactor]
            }
            return
        }
        guard Date().timeIntervalSince(sentAt) > 1.5 else { return }
        if now == "done" {
            running = false
            if !inSetup {
                if model.path.last == .secondFactor { model.back() }
            } else if model.changingAccount {
                model.finishSetup()
            } else {
                model.setup.go(.done)
            }
        } else if e == nil || now == "failed" {
            running = false
            // No run at all: the core was stopped meanwhile (iOS ended the app's time in the
            // background), so the code is used up.
            let text = e.map { model.strings.message($0.string("error"), $0.object("error_t")) } ?? model.t("enroll.failed")
            if inSetup && status.string("error_kind") == "password" {
                model.setup.go(.signIn)
                model.setup.error = text
            } else {
                error = text
            }
        }
    }
}
