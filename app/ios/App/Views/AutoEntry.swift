import SwiftUI

/// The second factor the short way (AutoEnroll), for setup and for Settings: the code the
/// authenticator app shows, then the core's progress. Role model, as on the desktop page:
/// Microsoft Authenticator's "sign in to add your work account". The key stays one tap away.
struct AutoEntry: View {
    let model: AppModel
    @Bindable var auto: AutoEnroll
    /// Setup sends the account with the first code; Settings only the code.
    let setup: SetupState?
    let byHand: () -> Void

    var body: some View {
        let busy = auto.busy(model.status)
        Form {
            Section {
                Text(auto.text(model))
                    // Said when it changes, like the page's aria-live line: "type the next code"
                    // asks for something.
                    .onChange(of: auto.text(model)) { _, new in announce(new) }
                if busy {
                    ProgressView().frame(maxWidth: .infinity)
                } else {
                    TextField(model.t("setup.code"), text: $auto.code, prompt: Text(verbatim: "123456"))
                        .keyboardType(.numberPad)
                        .textContentType(.oneTimeCode)
                }
            } header: {
                HStack {
                    Text(model.t("app.second_factor"))
                    Spacer()
                    InfoButton(model: model, text: model.t("setup.auto_help"))
                }
            } footer: {
                ErrorText(text: auto.error)
            }
            Section {
                Button(model.t("setup.by_hand")) {
                    auto.cancel()
                    byHand()
                }
                .disabled(auto.sending)
            }
        }
        .safeAreaInset(edge: .bottom) {
            if !busy {
                BottomButton(title: model.t("app.next")) {
                    if setup?.justChanged == true { return }
                    Task { await auto.send(model, setup: setup) }
                }
            }
        }
    }
}
