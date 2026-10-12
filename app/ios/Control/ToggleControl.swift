import AppIntents
import NetworkExtension
import SwiftUI
import WidgetKit

/// Control Center button (iOS 18): one tap turns Uni VPN on or off, like the Android app's
/// Quick Settings tile (role model: Tailscale's control). Before setup, or before the user
/// allowed the VPN configuration in the app, there is nothing to turn on: the tap does nothing
/// and the app is the way in.
@main
struct UniVPNControls: WidgetBundle {
    var body: some Widget {
        ToggleControl()
    }
}

struct ToggleControl: ControlWidget {
    var body: some ControlWidgetConfiguration {
        StaticControlConfiguration(kind: "de.davidvinu.univpn.toggle", provider: VPNState()) { on in
            ControlWidgetToggle("Uni VPN", isOn: on, action: SetVPN()) { isOn in
                Label {
                    Text(verbatim: "Uni VPN")
                } icon: {
                    Image(systemName: isOn ? "graduationcap.fill" : "graduationcap")
                }
            }
        }
        .displayName("Uni VPN")
    }
}

struct VPNState: ControlValueProvider {
    var previewValue: Bool { false }

    func currentValue() async throws -> Bool {
        guard let manager = try await NETunnelProviderManager.loadAllFromPreferences().first else { return false }
        let running: [NEVPNStatus] = [.connected, .connecting, .reasserting]
        return manager.isOnDemandEnabled || running.contains(manager.connection.status)
    }
}

struct SetVPN: SetValueIntent {
    static let title: LocalizedStringResource = "Uni VPN"

    @Parameter(title: "Uni VPN")
    var value: Bool

    func perform() async throws -> some IntentResult {
        let setupDone = UserDefaults(suiteName: "group.de.davidvinu.univpn")?.bool(forKey: "setup_done") ?? false
        guard setupDone, let manager = try await NETunnelProviderManager.loadAllFromPreferences().first else {
            return .result()
        }
        // The same as the app's switch: on demand keeps the VPN up.
        manager.isEnabled = true
        manager.isOnDemandEnabled = value
        try await manager.saveToPreferences()
        try await manager.loadFromPreferences()
        if value {
            try manager.connection.startVPNTunnel()
        } else {
            manager.connection.stopVPNTunnel()
        }
        return .result()
    }
}
