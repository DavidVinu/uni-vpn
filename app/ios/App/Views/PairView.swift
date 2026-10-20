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
        // "gone": the code on screen is no longer in the status. The core stopped meanwhile (the
        // app left the screen while the VPN was off) and took the code back with it.
        let pairingState = pairing != nil && pairing?.string("code") == shown
            ? pairing?.string("state") ?? "" : (shown == nil ? "" : "gone")
        Form {
            if done {
                Section {
                    VStack(spacing: 16) {
                        Image(systemName: "checkmark.circle.fill")
                            .font(.system(size: 64))
                            .foregroundStyle(.green)
                        Text(model.t("sync.done")).font(.title3.weight(.semibold))
                        // Linked, but some settings could not be used here.
                        ErrorText(text: error)
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
                        } else if error == nil && !busy {
                            ProgressView()
                        }
                        // A scanned code on its way: the scanner closed, nothing else shows it.
                        if busy { ProgressView() }
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
                .disabled(busy)
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
        .onChange(of: pairingState) { old, state in
            // Typing a code: the one shown before is not on screen, only its "done" counts. Linked
            // (also by a scanned code): what becomes of the code on screen no longer matters.
            if done || (typing && state != "done") { return }
            switch state {
            case "done":
                done = true
                // No earlier error under the check mark; what could not be used here, if anything.
                error = model.status?.object("sync")?.object("pairing")?.object("error").map { model.strings.t($0) }
            case "expired": error = model.t("sync.expired")
            // Only after it was there: a status from before the code counts for nothing.
            case "gone" where old == "waiting": error = model.t("sync.expired")
            case "failed": error = model.t("sync.offline")
            default: break
            }
        }
        // The phone stays awake while its code waits: locked, the app stops its core (VPN off)
        // and the code goes while the other device is still typing it. Expired, it locks again.
        .onChange(of: pairingState == "waiting" && !done && !typing, initial: true) { _, showing in
            UIApplication.shared.isIdleTimerDisabled = showing
        }
        .onDisappear {
            UIApplication.shared.isIdleTimerDisabled = false
            Task { _ = await API.post("/api/sync/cancel") }
        }
    }

    private func enter(_ code: String) async {
        // One code at a time: scanned again while the first is on its way, the code would
        // already be used up ("unknown").
        guard !busy else { return }
        busy = true
        error = nil // the same error again is said again
        let before = others()
        let result = await model.post("/api/sync/enter", ["code": code], timeout: 60)
        busy = false
        // Linked meanwhile through the code on screen: its check mark stays as it is.
        if done { return }
        // No answer in time (a slow relay), but a device syncs with this phone that did not before
        // (a code sent to another device starts a group of this phone alone; joining another
        // group can keep the count): linked all the same, not "not running".
        if !result.ok && !(result.reply.status == 0 && !others().subtracting(before).isEmpty) {
            error = result.error
            return
        }
        done = true
        error = result.json.map { model.strings.message($0.string("error"), $0.object("error_t")) }
    }

    /// The other devices that sync with this phone; none while syncing is off here.
    private func others() -> Set<String> {
        guard let sync = model.status?.object("sync"), sync.bool("on") else { return [] }
        return Set(sync.objects("devices").filter { !$0.bool("this") }.map { $0.string("id") })
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
