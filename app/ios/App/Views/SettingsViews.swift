import SwiftUI

/// Role model: iOS's own Settings app, a grouped list with the value on the right.
struct SettingsView: View {
    @Bindable var model: AppModel
    @State private var password = ""
    @State private var passwordError: String?
    /// Why Remove did not go through, with the device it was for.
    @State private var removeError: (device: String, text: String)?

    var body: some View {
        let status = model.status ?? JSON([:])
        let sync = status.object("sync")
        List {
            Section(model.t("settings.account")) {
                row(model.t("app.university"), status.string("university_name")) { changeAccount() }
                row(model.t("app.username"), status.string("user")) { changeAccount() }
                row(model.t("app.password"), "••••••••") {
                    passwordError = nil
                    model.passwordDialog = true
                }
                if SetupState.totpModes.contains(status.string("mfa")) {
                    NavigationLink(model.t("app.second_factor"), value: Screen.secondFactor)
                }
            }
            Section(model.t("settings.websites")) {
                NavigationLink(value: Screen.websites) {
                    LabeledContent(model.t("settings.through"),
                                   value: model.strings.plural("settings.domain_count", status.strings("domains").count))
                }
            }
            if let sync, sync.bool("available") {
                Section {
                    if sync.bool("on") {
                        // Remove on every row, this phone's too (turns syncing off here), as on the desktop.
                        ForEach(sync.objects("devices"), id: \.deviceID) { device in
                            HStack {
                                Text(device.string("name"))
                                Spacer()
                                if device.bool("this") {
                                    Text(model.t("sync.this")).foregroundStyle(.secondary)
                                }
                                Button(model.t("sync.remove"), role: .destructive) {
                                    Task {
                                        let id = device.string("id")
                                        let result = await model.post("/api/sync/remove", ["device": id], timeout: 30)
                                        removeError = result.error.map { (device: id, text: $0) }
                                    }
                                }
                                .buttonStyle(.borderless)
                            }
                        }
                    }
                    Button {
                        model.open(.pair("offer"))
                    } label: {
                        Label(model.t("sync.add"), systemImage: "plus")
                    }
                } header: {
                    Text(model.t("settings.devices"))
                } footer: {
                    // Why a removal or the last sync did not go through, as on the desktop.
                    let problem = sync.bool("on") ? sync.object("error") : nil
                    // An answer that came too late (a slow relay) while the device went all the
                    // same is no error: only while it is still listed.
                    let listed: [String] = sync.bool("on") ? sync.objects("devices").map { $0.string("id") } : []
                    let removal: String? = removeError.flatMap { listed.contains($0.device) ? $0.text : nil }
                    ErrorText(text: removal ?? problem.map { model.strings.t($0) })
                }
            }
            Section(model.t("settings.general")) {
                let setting = status.string("language")
                let name = model.strings.languages.first { $0.code == setting }?.name ?? model.t("settings.language_system")
                NavigationLink(value: Screen.language) {
                    LabeledContent(model.t("settings.language"), value: name)
                }
            }
            Section(model.t("settings.about")) {
                NavigationLink(model.t("settings.log"), value: Screen.log)
                NavigationLink(model.t("phone.licenses"), value: Screen.licenses)
                LabeledContent(model.t("settings.version"), value: status.string("version"))
            }
        }
        .navigationTitle(model.t("settings.title"))
        .task {
            // Like the desktop: opening Settings brings in changes from the other devices.
            if sync?.bool("on") == true { _ = await model.post("/api/sync/refresh", timeout: 30) }
        }
        .alert(model.t("app.password"), isPresented: $model.passwordDialog) {
            SecureField(model.t("app.password"), text: $password)
            Button(model.t("app.back"), role: .cancel) { password = "" }
            Button(model.t("app.save")) { savePassword() }
        } message: {
            if let passwordError { Text(passwordError) }
        }
    }

    private func row(_ title: String, _ value: String, action: @escaping () -> Void) -> some View {
        Button(action: action) {
            LabeledContent(title, value: value)
        }
        .foregroundStyle(.primary)
    }

    /// Setup again from the account as it is, with a fresh run: one that ended without its last
    /// page (an answer that came too late) would open on its old page.
    private func changeAccount() {
        model.setup = SetupState()
        model.changingAccount = true
    }

    private func savePassword() {
        let entered = password
        password = ""
        guard !entered.isEmpty else { return }
        Task {
            let result = await model.post("/api/password", ["password": entered])
            if !result.ok {
                // The alert closed with the tap: it comes back with the reason.
                passwordError = result.error
                model.passwordDialog = true
            }
        }
    }
}

private extension JSON {
    var deviceID: String { string("id") + "|" + string("name") }
}

/// The websites that go through the university: a list to add to and remove from (swipe).
struct WebsitesView: View {
    let model: AppModel
    @State private var adding = ""
    @State private var error: String?

    var body: some View {
        let domains = model.status?.strings("domains") ?? []
        List {
            Section {
                ForEach(domains, id: \.self) { host in
                    Label(host, systemImage: "globe")
                }
                .onDelete { offsets in
                    save(domains.enumerated().filter { !offsets.contains($0.offset) }.map(\.element))
                }
            }
            Section {
                HStack {
                    TextField("uni-heidelberg.de", text: $adding)
                        .keyboardType(.URL)
                        .textInputAutocapitalization(.never)
                        .autocorrectionDisabled()
                        .submitLabel(.done)
                        .onSubmit { add(domains) }
                    Button {
                        add(domains)
                    } label: {
                        Image(systemName: "plus.circle.fill")
                    }
                    .buttonStyle(.borderless)
                    .disabled(adding.trimmingCharacters(in: .whitespaces).isEmpty)
                    .accessibilityLabel(model.t("app.save"))
                }
            } footer: {
                ErrorText(text: error)
            }
        }
        .navigationTitle(model.t("settings.through"))
        .toolbar {
            ToolbarItem(placement: .topBarTrailing) {
                InfoButton(model: model, text: model.t("settings.domains_help"))
            }
        }
    }

    private func add(_ domains: [String]) {
        let host = adding.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !host.isEmpty else { return }
        save(domains + [host]) { adding = "" }
    }

    private func save(_ list: [String], then: @escaping () -> Void = {}) {
        Task {
            let result = await model.post("/api/domains", ["text": list.joined(separator: "\n")])
            error = result.error
            if result.ok {
                model.domainsChanged()
                then()
            }
        }
    }
}

struct SecondFactorView: View {
    let model: AppModel
    @State private var secret = ""
    @State private var valid = false
    @State private var error: String?
    /// The university takes one code instead of the key (AutoEnroll); nil until known.
    @State private var offersAuto: Bool?
    @State private var byHand = false

    var body: some View {
        Group {
            if offersAuto == nil && !model.auto.running {
                ProgressView()
            } else if (offersAuto == true || model.auto.running) && !byHand {
                AutoEntry(model: model, auto: model.auto, setup: nil) { byHand = true }
            } else {
                keyForm
            }
        }
        .navigationTitle(model.t("app.second_factor"))
        .task {
            let id = model.status?.string("university") ?? ""
            let list = await model.get("/universities.json")?.objects("universities") ?? []
            offersAuto = list.contains { $0.string("id") == id && $0.string("mfa_enroll") == "linotp" }
        }
        // Leaving stops a run started here, as Back does on the desktop page, and drops a typed
        // code and its error (a finished setup's run leaves inSetup set).
        .onDisappear { if !(model.auto.running && model.auto.inSetup) { model.auto.cancel() } }
    }

    private var keyForm: some View {
        Form {
            Section {
                TotpEntry(model: model, secret: $secret, valid: $valid, error: error)
            }
        }
        .toolbar {
            ToolbarItem(placement: .topBarTrailing) {
                InfoButton(model: model, text: model.t("setup.totp_help"))
            }
        }
        .safeAreaInset(edge: .bottom) {
            BottomButton(title: model.t("app.save"), enabled: valid) {
                Task {
                    let result = await model.post("/api/totp", ["secret": secret.trimmingCharacters(in: .whitespacesAndNewlines)])
                    if result.ok { model.back() } else { error = result.error }
                }
            }
        }
    }
}

struct LanguageView: View {
    let model: AppModel

    var body: some View {
        let setting = model.status?.string("language") ?? ""
        let choices = [(code: "", name: model.t("settings.language_system"))] + model.strings.languages
        List {
            ForEach(choices.indices, id: \.self) { i in
                let choice = choices[i]
                Button {
                    Task { _ = await model.post("/api/language", ["language": choice.code]) }
                } label: {
                    HStack {
                        Text(choice.name).foregroundStyle(.primary)
                        Spacer()
                        if choice.code == setting {
                            Image(systemName: "checkmark").foregroundStyle(.tint)
                        }
                    }
                }
            }
        }
        .navigationTitle(model.t("settings.language"))
    }
}

struct LogView: View {
    let model: AppModel

    var body: some View {
        ScrollView([.vertical, .horizontal]) {
            Text((model.status?.strings("log_tail") ?? []).joined(separator: "\n"))
                .font(.system(size: 11, design: .monospaced))
                .textSelection(.enabled)
                .padding()
        }
        .navigationTitle(model.t("settings.log"))
        .navigationBarTitleDisplayMode(.inline)
    }
}

/// The licences of openconnect, OpenSSL and the rest the app carries (build/licenses, copied in
/// by packaging/ios/build-core.sh).
struct LicensesView: View {
    let model: AppModel

    private var files: [URL] {
        guard let folder = Bundle.main.url(forResource: "licenses", withExtension: nil),
              let names = try? FileManager.default.contentsOfDirectory(at: folder, includingPropertiesForKeys: nil)
        else { return [] }
        return names.sorted { $0.lastPathComponent < $1.lastPathComponent }
    }

    var body: some View {
        List(files, id: \.self) { file in
            NavigationLink(LicensesView.title(file)) {
                ScrollView {
                    Text((try? String(contentsOf: file, encoding: .utf8)) ?? "")
                        .font(.system(size: 11, design: .monospaced))
                        .textSelection(.enabled)
                        .padding()
                }
                .navigationTitle(LicensesView.title(file))
                .navigationBarTitleDisplayMode(.inline)
            }
        }
        .navigationTitle(model.t("phone.licenses"))
    }

    /// "golang.org_x_net-LICENSE" reads "golang.org/x/net".
    static func title(_ file: URL) -> String {
        var name = file.lastPathComponent
        for marker in ["-LICENSE", "-LICENCE", "-COPYING", "-Copyright"] {
            if let range = name.range(of: marker) { name = String(name[..<range.lowerBound]) }
        }
        return name.replacingOccurrences(of: "_", with: "/")
    }
}
