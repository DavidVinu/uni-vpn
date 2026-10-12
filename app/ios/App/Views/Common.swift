import SwiftUI

/// A hint that would otherwise be text on the screen: behind an info button (project rule).
struct InfoButton: View {
    let model: AppModel
    let text: String
    @State private var open = false

    var body: some View {
        Button {
            open = true
        } label: {
            Image(systemName: "info.circle")
        }
        .buttonStyle(.borderless)
        .accessibilityLabel(model.t("app.help"))
        .alert(model.t("app.help"), isPresented: $open) {
            Button(model.t("app.done")) {}
        } message: {
            Text(text)
        }
    }
}

/// A password that can be shown, as in iOS's own password fields.
struct PasswordField: View {
    let model: AppModel
    let label: String
    @Binding var text: String
    var showLabel = "app.show_password"
    @State private var shown = false

    var body: some View {
        HStack {
            Group {
                if shown {
                    TextField(label, text: $text)
                } else {
                    SecureField(label, text: $text)
                }
            }
            .textInputAutocapitalization(.never)
            .autocorrectionDisabled()
            Button {
                shown.toggle()
            } label: {
                Image(systemName: shown ? "eye.slash" : "eye")
                    .foregroundStyle(.secondary)
            }
            .buttonStyle(.borderless)
            .accessibilityLabel(model.t(showLabel))
        }
    }
}

/// An error under a group of fields.
struct ErrorText: View {
    let text: String?

    var body: some View {
        if let text, !text.isEmpty {
            Text(text).foregroundStyle(.red)
        }
    }
}

/// The big button at the bottom of a setup page.
struct BottomButton: View {
    let title: String
    var enabled = true
    var busy = false
    let action: () -> Void

    var body: some View {
        Button(action: action) {
            HStack {
                if busy { ProgressView().tint(.white) }
                Text(title)
            }
            .frame(maxWidth: .infinity)
        }
        .buttonStyle(.borderedProminent)
        .controlSize(.large)
        .disabled(!enabled || busy)
        .padding(.horizontal, 20)
        .padding(.bottom, 12)
    }
}

enum Look {
    static func color(_ state: String) -> Color {
        switch state {
        case "connected": return .green
        case "connecting", "disconnecting", "offline", "blocked": return .orange
        case "auth_failed", "keyring", "error": return .red
        default: return .gray
        }
    }
}
