import SwiftUI

private let userPattern = #/^[A-Za-z0-9][A-Za-z0-9._-]{0,63}(@[A-Za-z0-9][A-Za-z0-9.-]{0,252})?$/#

/// The setup assistant of the desktop app, on a phone. Role models: WhatsApp and Signal (a phone
/// joins by scanning the QR code another device shows), eduroam CAT (find your institution),
/// Google Authenticator (scan the setup key), Tailscale (the system's VPN prompt at the end).
/// Everything entered lives in the model (SetupState), so nothing is lost when the view is made
/// again.
struct SetupView: View {
    let model: AppModel

    var body: some View {
        let setup = model.setup
        NavigationStack {
            page(setup)
                .navigationTitle(title(setup))
                .navigationBarTitleDisplayMode(.inline)
                .toolbar {
                    if let back = back(setup) {
                        ToolbarItem(placement: .topBarLeading) {
                            Button {
                                back()
                            } label: {
                                Label(model.t("app.back"), systemImage: "chevron.left")
                            }
                        }
                    }
                }
        }
        // Once per setup run, also when a finished run hands over a fresh state; then the list.
        .task(id: ObjectIdentifier(setup)) {
            setup.prepare(model)
            await setup.loadUniversities(model)
        }
    }

    private func title(_ setup: SetupState) -> String {
        let n = setup.step.rawValue
        guard n >= 0 && n < SetupState.Step.done.rawValue else { return "" }
        // Without a second factor its step is skipped: connecting is the last one (Android).
        let shown = setup.step == .connecting && !setup.needsTotp ? setup.total : n + 1
        return model.t("setup.step", ["n": shown, "total": setup.total])
    }

    private func back(_ setup: SetupState) -> (() -> Void)? {
        switch setup.step {
        case .signIn: return { setup.go(.university) }
        case .secondFactor:
            // The key by hand goes back to the one code, as on the desktop page.
            if setup.byHand && setup.offersAuto {
                return {
                    setup.byHand = false
                    setup.error = nil
                }
            }
            if model.auto.sending { return nil }
            return {
                model.auto.cancel()
                setup.go(.signIn)
            }
        case .code:
            if setup.busy { return nil } // the code is on its way
            return { setup.go(.welcome) }
        case .university:
            if model.changingAccount { return { model.finishSetup() } }
            if model.status?.object("sync")?.bool("available") == true {
                return {
                    setup.dropDetect()
                    setup.go(.welcome)
                }
            }
            return nil
        case .connecting:
            if setup.sending { return nil }
            return {
                setup.sentAt = nil
                setup.go(setup.needsTotp ? .secondFactor : .signIn)
            }
        default:
            return nil
        }
    }

    @ViewBuilder
    private func page(_ setup: SetupState) -> some View {
        switch setup.step {
        case .welcome: WelcomePage(model: model, setup: setup)
        case .code: CodePage(model: model, setup: setup)
        case .university:
            // An account change waits for the list, as on the desktop page.
            if model.changingAccount && setup.universities.isEmpty {
                ProgressView()
            } else {
                UniversityPage(model: model, setup: setup)
            }
        case .signIn: SignInPage(model: model, setup: setup)
        case .secondFactor: SecondFactorPage(model: model, setup: setup)
        case .connecting: ConnectingPage(model: model, setup: setup)
        case .done: DonePage(model: model)
        }
    }
}

private struct WelcomePage: View {
    let model: AppModel
    let setup: SetupState

    var body: some View {
        VStack(spacing: 16) {
            Spacer()
            Image("Logo")
                .resizable()
                .frame(width: 112, height: 112)
                .clipShape(RoundedRectangle(cornerRadius: 25, style: .continuous))
            Text("Uni VPN").font(.largeTitle.weight(.bold))
            Spacer()
            VStack(spacing: 12) {
                if QRScanner.supported {
                    HStack {
                        ScanButton(model: model, title: model.t("phone.scan_code")) { value in
                            guard value.hasPrefix("UNIVPN:") else { return }
                            setup.pairCode = String(value.dropFirst("UNIVPN:".count))
                            setup.go(.code)
                            Task { await setup.enterCode(model) }
                        }
                        .buttonStyle(.borderedProminent)
                        .controlSize(.large)
                        InfoButton(model: model, text: model.t("phone.scan_code_help"))
                    }
                }
                Button(model.t("sync.enter")) { setup.go(.code) }
                Button(model.t("phone.setup_here")) { setup.go(.university) }
            }
            .padding(.bottom, 24)
        }
        .frame(maxWidth: .infinity)
        .padding(.horizontal, 24)
    }
}

private struct CodePage: View {
    let model: AppModel
    @Bindable var setup: SetupState

    var body: some View {
        Form {
            Section {
                TextField("XXXX-XXXX-XXXX", text: $setup.pairCode)
                    .textInputAutocapitalization(.characters)
                    .autocorrectionDisabled()
                    .onChange(of: setup.pairCode) { _, value in setup.pairCode = value.uppercased() }
            } header: {
                Text(model.t("sync.use_other"))
            } footer: {
                ErrorText(text: setup.error)
            }
        }
        .safeAreaInset(edge: .bottom) {
            BottomButton(title: model.t("app.next"),
                         enabled: !setup.pairCode.trimmingCharacters(in: .whitespaces).isEmpty,
                         busy: setup.busy) {
                Task { await setup.enterCode(model) }
            }
        }
    }
}

private struct UniversityPage: View {
    let model: AppModel
    @Bindable var setup: SetupState

    var body: some View {
        Form {
            Section {
                TextField(model.t("setup.find_placeholder"), text: $setup.queryText)
                    .autocorrectionDisabled()
            } header: {
                HStack {
                    Text(model.t("setup.find"))
                    Spacer()
                    InfoButton(model: model, text: model.t("setup.find_help"))
                }
            }
            if setup.uniId.isEmpty {
                Section {
                    ForEach(setup.found()) { u in
                        Button {
                            setup.uniId = u.id
                            setup.query = ""
                            setup.dropDetect()
                            setup.error = u.mfa == "saml" ? model.t("setup.saml") : nil
                        } label: {
                            LabeledContent(u.name, value: u.country)
                        }
                        .foregroundStyle(.primary)
                    }
                    Button(model.t("setup.not_listed")) {
                        setup.uniId = "other"
                        setup.query = ""
                        setup.dropDetect()
                        setup.error = nil
                    }
                }
            }
            if setup.isOther {
                Section {
                    TextField("vpn.example.edu", text: $setup.hostText)
                        .keyboardType(.URL)
                        .textInputAutocapitalization(.never)
                        .autocorrectionDisabled()
                    if setup.showsGroups {
                        Picker(model.t("setup.group"), selection: $setup.pickedGroup) {
                            ForEach(setup.groups, id: \.self) { Text($0).tag($0) }
                        }
                    }
                    if !setup.otherMfa.isEmpty {
                        Picker(model.t("app.second_factor"), selection: $setup.otherMfa) {
                            ForEach(["none", "totp_field", "totp_append", "duo_push"], id: \.self) { m in
                                Text(model.t("setup.mfa.\(m)")).tag(m)
                            }
                        }
                    }
                } header: {
                    Text(model.t("setup.vpn_address"))
                }
            }
            if setup.error != nil {
                Section {
                    ErrorText(text: setup.error)
                }
            }
        }
        .safeAreaInset(edge: .bottom) {
            BottomButton(title: model.t("app.next"), busy: setup.detecting) { next() }
        }
    }

    private func next() {
        if setup.uniId.isEmpty {
            setup.error = model.t("setup.choose")
        } else if setup.uni?.mfa == "saml" {
            setup.error = model.t("setup.saml")
        } else if setup.isOther && setup.host.trimmingCharacters(in: .whitespaces).isEmpty {
            setup.error = model.t("setup.enter_address")
        } else if setup.isOther && setup.otherMfa.isEmpty {
            Task { await setup.detect(model, group: setup.showsGroups ? setup.group : "") }
        } else {
            setup.go(.signIn)
        }
    }
}

private struct SignInPage: View {
    let model: AppModel
    @Bindable var setup: SetupState

    var body: some View {
        Form {
            Section {
                TextField(model.t("app.username"), text: $setup.user, prompt: Text(verbatim: "ab123"))
                    .textContentType(.username)
                    .keyboardType(.emailAddress)
                    .textInputAutocapitalization(.never)
                    .autocorrectionDisabled()
                PasswordField(model: model, label: model.t("app.password"), text: $setup.password)
                    .textContentType(.password)
            } header: {
                Text(model.t("setup.sign_in"))
            } footer: {
                ErrorText(text: setup.error)
            }
        }
        .safeAreaInset(edge: .bottom) {
            BottomButton(title: model.t("app.next"), busy: setup.sending) { next() }
        }
    }

    private func next() {
        let user = setup.user.trimmingCharacters(in: .whitespacesAndNewlines)
        if user.wholeMatch(of: userPattern) == nil {
            setup.error = model.t("setup.enter_user")
        } else if setup.password.isEmpty {
            setup.error = model.t("setup.enter_password")
        } else if setup.needsTotp {
            setup.byHand = false
            setup.go(.secondFactor)
        } else {
            setup.submit(model)
        }
    }
}

private struct SecondFactorPage: View {
    let model: AppModel
    @Bindable var setup: SetupState

    var body: some View {
        if (setup.offersAuto || model.auto.running) && !setup.byHand {
            AutoEntry(model: model, auto: model.auto, setup: setup) {
                setup.byHand = true
                setup.error = nil
            }
        } else {
            keyForm
        }
    }

    private var keyForm: some View {
        Form {
            Section {
                if let portal = setup.uni?.portal, let url = URL(string: portal), !portal.isEmpty {
                    Link(model.t("setup.mfa_portal"), destination: url)
                }
                TotpEntry(model: model, mfa: setup.mfa, secret: $setup.secret, valid: $setup.secretValid, error: setup.error)
            } header: {
                HStack {
                    Text(model.t("app.second_factor"))
                    Spacer()
                    InfoButton(model: model, text: setup.mfaSteps(model).joined(separator: "\n") + "\n\n" + model.t("setup.totp_help"))
                }
            }
        }
        .safeAreaInset(edge: .bottom) {
            BottomButton(title: model.t("app.next"), enabled: setup.secretValid) { setup.submit(model) }
        }
    }
}

private struct ConnectingPage: View {
    let model: AppModel
    let setup: SetupState

    var body: some View {
        let status = model.status ?? JSON([:])
        let state = status.string("state")
        let stuck = ["offline", "blocked", "error"].contains(state)
        let text = stuck || status.string("message_id") == "waiting_for_code"
            ? model.strings.message(status.string("message"), status.object("message_t"))
            : model.t("setup.waiting")
        VStack(spacing: 16) {
            Spacer()
            ProgressView().controlSize(.large)
            Text(model.t("look.connecting")).font(.title2.weight(.semibold))
            Text(text)
                .multilineTextAlignment(.center)
                .foregroundStyle(.secondary)
            if stuck && setup.sentAt != nil {
                Button(model.t("setup.finish")) {
                    setup.sentAt = nil
                    if model.changingAccount { model.finishSetup() } else { setup.go(.done) }
                }
            }
            Spacer()
            Spacer()
        }
        .frame(maxWidth: .infinity)
        .padding(.horizontal, 24)
    }
}

private struct DonePage: View {
    let model: AppModel

    var body: some View {
        let hasDomains = !(model.status?.strings("domains").isEmpty ?? true)
        VStack(spacing: 16) {
            Spacer()
            Image(systemName: "checkmark.circle.fill")
                .font(.system(size: 72))
                .foregroundStyle(.green)
            Text(model.t("setup.all_set")).font(.title.weight(.bold))
            Text(model.t(hasDomains ? "setup.all_set_sub" : "setup.all_set_no_domains"))
                .multilineTextAlignment(.center)
                .foregroundStyle(.secondary)
            // Settings another device has that could not be used here (SetupState.enterCode).
            ErrorText(text: model.setup.error)
                .multilineTextAlignment(.center)
            Spacer()
            // Tailscale: the system's VPN prompt follows the sign-in.
            BottomButton(title: model.t("app.done")) {
                model.finishSetup()
                Task { await model.turnOn() }
            }
        }
        .padding(.horizontal, 4)
    }
}
