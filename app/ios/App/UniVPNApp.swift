import SwiftUI

@main
struct UniVPNApp: App {
    @State private var model = AppModel()
    @Environment(\.scenePhase) private var scenePhase

    var body: some Scene {
        WindowGroup {
            RootView(model: model)
        }
        .onChange(of: scenePhase, initial: true) { _, phase in
            switch phase {
            case .active: model.becameActive()
            case .background: model.wentToBackground()
            default: break
            }
        }
    }
}

/// Setup whenever it is needed or asked for, else Home with the other screens on top, like the
/// Android app's App.kt.
struct RootView: View {
    @Bindable var model: AppModel

    var body: some View {
        if model.portTaken {
            FitPage { PortTakenView(model: model) }
        } else if model.status == nil {
            ProgressView()
        } else if model.setupNeeded || model.changingAccount || model.setupFinishing {
            SetupView(model: model)
        } else {
            NavigationStack(path: $model.path) {
                HomeView(model: model)
                    .navigationDestination(for: Screen.self) { screen in
                        switch screen {
                        case .settings: SettingsView(model: model)
                        case .websites: WebsitesView(model: model)
                        case .secondFactor: SecondFactorView(model: model)
                        case .language: LanguageView(model: model)
                        case .log: LogView(model: model)
                        case .licenses: LicensesView(model: model)
                        case .pair(let mode): PairView(model: model, mode: mode)
                        }
                    }
            }
        }
    }
}

/// Instead of a spinner that never ends, like the Android app. The core's texts need the core:
/// this one comes with the app. The switch stays, so Uni VPN can still be turned off here.
private struct PortTakenView: View {
    @Bindable var model: AppModel

    var body: some View {
        let defaults = UserDefaults(suiteName: CoreHost.appGroup)
        let setupDone = defaults?.bool(forKey: "setup_done") ?? false
        let language = defaults?.string(forKey: "language") ?? ""
        VStack(spacing: 24) {
            Text(Strings.portTaken(language, Locale.preferredLanguages))
                .multilineTextAlignment(.center)
            if model.vpn.isOn || setupDone {
                HStack(spacing: 16) {
                    Text(verbatim: "Uni VPN").font(.headline)
                    Toggle("Uni VPN", isOn: $model.vpnSwitch)
                        .labelsHidden()
                        .disabled(model.switching)
                }
            }
        }
        .padding(24)
    }
}
