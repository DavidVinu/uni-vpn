import AVFoundation
import SwiftUI
import UIKit
import VisionKit

/// Scans one QR code with the camera, VisionKit's scanner: it finds the code by itself, nothing
/// to aim or tap. One camera prompt the first time (NSCameraUsageDescription).
struct QRScanner: UIViewControllerRepresentable {
    let onCode: (String) -> Void

    /// Phones without the scanner (older than the A12 chip) and the simulator type instead.
    @MainActor static var supported: Bool { DataScannerViewController.isSupported }

    /// Asks for the camera once; false when the user said no before.
    static func allowed() async -> Bool {
        switch AVCaptureDevice.authorizationStatus(for: .video) {
        case .authorized: return true
        case .notDetermined: return await AVCaptureDevice.requestAccess(for: .video)
        default: return false
        }
    }

    func makeUIViewController(context: Context) -> DataScannerViewController {
        let scanner = DataScannerViewController(
            recognizedDataTypes: [.barcode(symbologies: [.qr])],
            qualityLevel: .balanced,
            recognizesMultipleItems: false,
            isHighFrameRateTrackingEnabled: false,
            isHighlightingEnabled: true
        )
        scanner.delegate = context.coordinator
        return scanner
    }

    func updateUIViewController(_ scanner: DataScannerViewController, context: Context) {
        if !scanner.isScanning { try? scanner.startScanning() }
    }

    static func dismantleUIViewController(_ scanner: DataScannerViewController, coordinator: Coordinator) {
        scanner.stopScanning()
    }

    func makeCoordinator() -> Coordinator { Coordinator(onCode: onCode) }

    final class Coordinator: NSObject, DataScannerViewControllerDelegate {
        let onCode: (String) -> Void
        private var done = false

        init(onCode: @escaping (String) -> Void) { self.onCode = onCode }

        func dataScanner(_ dataScanner: DataScannerViewController, didAdd addedItems: [RecognizedItem], allItems: [RecognizedItem]) {
            guard !done else { return }
            for item in addedItems {
                if case .barcode(let code) = item, let text = code.payloadStringValue {
                    done = true
                    onCode(text)
                    return
                }
            }
        }
    }
}

/// The scanner as a sheet with a way back.
struct ScanSheet: View {
    let model: AppModel
    let onCode: (String) -> Void
    @Environment(\.dismiss) private var dismiss

    var body: some View {
        NavigationStack {
            QRScanner { code in
                dismiss()
                onCode(code)
            }
            .ignoresSafeArea()
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button(model.t("app.back")) { dismiss() }
                }
            }
        }
    }
}

/// A "Scan" button that opens the scanner, or nothing where there is none.
struct ScanButton: View {
    let model: AppModel
    let title: String
    let onCode: (String) -> Void
    @State private var scanning = false
    @State private var denied = false

    var body: some View {
        if QRScanner.supported {
            Button {
                // Said no before, or Screen Time blocks the camera: iOS does not ask again, only
                // Settings can allow it.
                if [.denied, .restricted].contains(AVCaptureDevice.authorizationStatus(for: .video)) {
                    denied = true
                    return
                }
                Task { if await QRScanner.allowed() { scanning = true } }
            } label: {
                Label(title, systemImage: "qrcode.viewfinder")
            }
            .sheet(isPresented: $scanning) {
                ScanSheet(model: model, onCode: onCode)
            }
            .alert(model.t("phone.camera_denied"), isPresented: $denied) {
                Button(model.t("app.back"), role: .cancel) {}
                Button(model.t("settings.title")) {
                    if let url = URL(string: UIApplication.openSettingsURLString) { UIApplication.shared.open(url) }
                }
            }
        }
    }
}
