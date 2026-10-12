import CoreImage
import CoreImage.CIFilterBuiltins
import SwiftUI

/// Add a device. Role models: Signal "Link a device" and WhatsApp linked devices: one device
/// shows a QR code with the code under it, the other scans it or types the code. The phone can
/// also scan the code a computer shows under "Use settings from another device".
struct PairView: View {
    let model: AppModel
    let mode: String
    @State private var shown: String?
    @State private var error: String?
    @State private var typing = false
    @State private var typed = ""
    @State private var done = false
    @State private var attempt = 0
    @State private var busy = false

    var body: some View {
        let pairing = model.status?.object("sync")?.object("pairing")
        let pairingState = pairing != nil && pairing?.string("code") == shown ? pairing?.string("state") ?? "" : ""
        Form {
            if done {
                Section {
                    VStack(spacing: 16) {
                        Image(systemName: "checkmark.circle.fill")
                            .font(.system(size: 64))
                            .foregroundStyle(.green)
                        Text(model.t("sync.done")).font(.title3.weight(.semibold))
                    }
                    .frame(maxWidth: .infinity)
                    .padding(.vertical, 24)
                }
            } else if typing {
                Section {
                    TextField("XXXX-XXXX-XXXX", text: $typed)
                        .textInputAutocapitalization(.characters)
                        .autocorrectionDisabled()
                        .onChange(of: typed) { _, value in typed = value.uppercased() }
                } header: {
                    Text(model.t("sync.code"))
                } footer: {
                    ErrorText(text: error)
                }
            } else {
                Section {
                    VStack(spacing: 12) {
                        if let code = shown {
                            if let image = PairView.qr("UNIVPN:" + code.replacingOccurrences(of: "-", with: "")) {
                                Image(uiImage: image)
                                    .interpolation(.none)
                                    .resizable()
                                    .frame(width: 220, height: 220)
                                    .accessibilityLabel(code)
                            }
                            Text(code).font(.title2.monospaced())
                            Text(model.t(mode == "offer" ? "sync.show_offer" : "sync.show_request"))
                                .font(.subheadline)
                                .foregroundStyle(.secondary)
                                .multilineTextAlignment(.center)
                        } else if error == nil {
                            ProgressView()
                        }
                    }
                    .frame(maxWidth: .infinity)
                    .padding(.vertical, 12)
                } footer: {
                    ErrorText(text: error)
                }
                Section {
                    ScanButton(model: model, title: model.t("phone.scan_code")) { value in
                        guard value.hasPrefix("UNIVPN:") else { return }
                        Task { await enter(String(value.dropFirst("UNIVPN:".count))) }
                    }
                    Button(model.t("sync.enter")) {
                        typing = true
                        error = nil
                    }
                    if error != nil {
                        Button(model.t("sync.new_code")) { attempt += 1 }
                    }
                }
            }
        }
        .navigationTitle(model.t(mode == "offer" ? "sync.add" : "sync.use_other"))
        .navigationBarTitleDisplayMode(.inline)
        .safeAreaInset(edge: .bottom) {
            if done {
                BottomButton(title: model.t("app.done")) { model.back() }
            } else if typing {
                BottomButton(title: model.t("app.next"), enabled: !typed.trimmingCharacters(in: .whitespaces).isEmpty, busy: busy) {
                    Task { await enter(typed) }
                }
            }
        }
        .task(id: attempt) {
            error = nil
            shown = nil
            let result = await model.post("/api/sync/\(mode)", timeout: 30)
            if result.ok {
                shown = result.json?.object("pairing")?.string("code")
            } else {
                error = result.error
            }
        }
        .onChange(of: pairingState) { _, state in
            switch state {
            case "done": done = true
            case "expired": error = model.t("sync.expired")
            case "failed": error = model.t("sync.offline")
            default: break
            }
        }
        .onDisappear {
            Task { _ = await API.post("/api/sync/cancel") }
        }
    }

    private func enter(_ code: String) async {
        busy = true
        let result = await model.post("/api/sync/enter", ["code": code], timeout: 60)
        busy = false
        if result.ok { done = true } else { error = result.error }
    }

    /// Black modules on white with the standard quiet zone, error correction M (docs/sync.md).
    static func qr(_ text: String) -> UIImage? {
        let filter = CIFilter.qrCodeGenerator()
        filter.message = Data(text.utf8)
        filter.correctionLevel = "M"
        guard let output = filter.outputImage else { return nil }
        // Four modules of white around the code.
        let framed = output.transformed(by: CGAffineTransform(translationX: 4, y: 4))
            .composited(over: CIImage(color: .white).cropped(to: output.extent.insetBy(dx: -4, dy: -4).offsetBy(dx: 4, dy: 4)))
        let scaled = framed.transformed(by: CGAffineTransform(scaleX: 8, y: 8))
        guard let cg = CIContext().createCGImage(scaled, from: scaled.extent) else { return nil }
        return UIImage(cgImage: cg)
    }
}
