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
        if model.status == nil {
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
