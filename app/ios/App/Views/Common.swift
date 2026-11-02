import Accessibility
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

/// Says a text that appeared by itself: iOS has no live regions (Android's polite region, the
/// page's aria-live), so VoiceOver would not read it.
func announce(_ text: String) {
    var spoken = AttributedString(text)
    spoken.accessibilitySpeechAnnouncementPriority = .low // after what VoiceOver says (a new page)
    AccessibilityNotification.Announcement(spoken).post()
}

/// An error under a group of fields, said when it appears or changes, or `attempt` counts up
/// (the same error after another try); `live` false while a key is typed letter by letter.
struct ErrorText: View {
    let text: String?
    var live = true
    var attempt = 0

    var body: some View {
        if let text, !text.isEmpty {
            Text(text).foregroundStyle(.red)
                .onChange(of: Said(text: text, attempt: attempt), initial: true) { _, new in
                    if live { announce(new.text) }
                }
        }
    }

    private struct Said: Equatable {
        let text: String
        let attempt: Int
    }
}

/// A page laid out as it is when it fits, scrolling when it does not: at the largest
/// accessibility text sizes on a small iPhone its texts would otherwise be cut off.
struct FitPage<Content: View>: View {
    @ViewBuilder let content: Content

    var body: some View {
        ViewThatFits(in: .vertical) {
            content
            ScrollView { content }
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
