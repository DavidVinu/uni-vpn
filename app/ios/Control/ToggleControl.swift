import AppIntents
import NetworkExtension
import SwiftUI
import WidgetKit

/// Control Center button (iOS 18): one tap turns Uni VPN on or off, like the Android app's
/// Quick Settings tile (role model: Tailscale's control). Before setup, or before the user
/// allowed the VPN configuration in the app, there is nothing to turn on: the tap does nothing
/// and the app is the way in. The tap's intent (SetVPN.swift) runs in the app's process.
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
        // Reading the configuration may be the app's privilege; then what the app last wrote.
        guard let managers = try? await NETunnelProviderManager.loadAllFromPreferences() else {
            return UserDefaults(suiteName: SetVPN.appGroup)?.bool(forKey: SetVPN.onKey) ?? false
        }
        guard let manager = managers.first else { return false }
        let running: [NEVPNStatus] = [.connected, .connecting, .reasserting]
        // As the app's switch: another VPN app turned on disables this configuration.
        return (manager.isEnabled && manager.isOnDemandEnabled) || running.contains(manager.connection.status)
    }
}
