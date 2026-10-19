import SwiftUI

/// The setup key of the second factor, role model Google Authenticator: scan the QR code of the
/// university's security page, or type the key. A valid key shows the current test code with
/// the time it has left, for the security page's "test" step. Rows of a Form section. `mfa` is
/// the sign-in mode being set up: a key whose codes it can't use is refused before the user
/// registers it.
struct TotpEntry: View {
    let model: AppModel
    let mfa: String
    @Binding var secret: String
    @Binding var valid: Bool
    let error: String?
    @State private var code: String?
    @State private var left = 0
    @State private var checkError: String?
    @State private var checks = 0

    var body: some View {
        ScanButton(model: model, title: model.t("phone.scan_qr")) { secret = $0 }
        PasswordField(model: model, label: model.t("setup.token_secret"), text: $secret, showLabel: "setup.show_secret")
            .task(id: "\(secret)|\(mfa)|\(checks)") { await check() }
        ErrorText(text: checkError ?? error)
        if let code {
            HStack(spacing: 12) {
                ZStack {
                    Circle().stroke(Color.secondary.opacity(0.25), lineWidth: 3)
                    Circle()
                        .trim(from: 0, to: CGFloat(left) / 30)
                        .stroke(Color.accentColor, style: StrokeStyle(lineWidth: 3, lineCap: .round))
                        .rotationEffect(.degrees(-90))
                        .animation(.linear(duration: 1), value: left)
                }
                .frame(width: 24, height: 24)
                // In halves: 123 456, or 1234 5678 (Marburg).
                Text(String(code.prefix(code.count / 2)) + " " + String(code.dropFirst(code.count / 2)))
                    .font(.title.monospacedDigit())
                Spacer()
                Text(model.t("setup.check_code"))
                    .foregroundStyle(.secondary)
            }
        }
    }

    private func check() async {
        code = nil
        valid = false
        checkError = nil
        let trimmed = secret.trimmingCharacters(in: .whitespacesAndNewlines)
        if trimmed.isEmpty { return }
        try? await Task.sleep(nanoseconds: 300_000_000)
        if Task.isCancelled { return }
        var result = await model.post("/api/totp/check", ["secret": trimmed, "mfa": mfa])
        // Back from another app, the app's core may still be starting (it stops in the background).
        for _ in 0..<10 where result.reply.status == 0 && !Task.isCancelled {
            try? await Task.sleep(nanoseconds: 500_000_000)
            result = await model.post("/api/totp/check", ["secret": trimmed, "mfa": mfa])
        }
        if Task.isCancelled { return }
        guard result.ok else {
            checkError = result.error
            return
        }
        valid = true
        code = result.json?.string("code")
        // By the clock: iOS suspends the app in the background, and a sleep then ends only once
        // it is back, so counting sleeps would show an old code as current.
        let remaining = Double(result.json?.int("remaining", 30) ?? 30)
        let end = Date().addingTimeInterval(remaining)
        while true {
            // A clock set back meanwhile (corrected by hand) ends the code too.
            let rest = end.timeIntervalSinceNow
            left = rest > remaining ? 0 : max(0, Int(rest.rounded(.up)))
            if left == 0 { break }
            try? await Task.sleep(nanoseconds: 1_000_000_000)
            if Task.isCancelled { return }
        }
        checks += 1 // the next code
    }
}
