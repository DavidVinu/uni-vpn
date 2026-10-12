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
    var otherMfa = ""
    var detecting = false
    var user = ""
    var password = ""
    var secret = ""
    var secretValid = false
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
    var mfa: String { isOther ? otherMfa : (uni?.mfa ?? "totp_field") }
    var needsTotp: Bool { SetupState.totpModes.contains(mfa) }
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
        if model.changingAccount {
            uniId = status.string("university")
            user = status.string("user")
        }
        Task {
            let list = await model.get("/universities.json")?.objects("universities") ?? []
            universities = list.map(University.init)
        }
    }

    func go(_ next: Step) {
        error = nil
        step = next
    }

    func submit(_ model: AppModel) {
        if sending { return }
        model.setupFinishing = true
        go(.connecting)
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
            if groups.count > 1 && !group.isEmpty { profile["authgroup"] = group }
        }
        let body: [String: Any] = [
            "user": user.trimmingCharacters(in: .whitespacesAndNewlines),
            "password": password,
            "secret": needsTotp ? secret.trimmingCharacters(in: .whitespacesAndNewlines) : "",
            "university": uniId,
            "profile": profile,
        ]
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

    private func failed(_ model: AppModel, _ result: PostResult) {
        if !model.changingAccount { model.setupFinishing = false }
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
        let result = await model.post("/api/sync/enter", ["code": pairCode], timeout: 60)
        busy = false
        if result.ok {
            model.setupFinishing = true
            go(.done)
        } else {
            error = result.error
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
        if d.has("error") {
            error = model.strings.message(d.string("error"), d.object("error_t"))
            return
        }
        if d.bool("saml") {
            error = model.t("setup.saml")
            return
        }
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
