import AppIntents
import Foundation
import NetworkExtension

/// What the Control Center button does. This file belongs to the app target as well as to the
/// Control extension: an App Intent that the app itself contains runs in the app's process
/// (iOS launches it in the background if needed), which may change the VPN configuration and
/// hand the core's lock over; the extension only shows the button.
struct SetVPN: SetValueIntent {
    static let title: LocalizedStringResource = "Uni VPN"
    static let appGroup = "group.de.davidvinu.univpn"
    /// The switch as the app last saw it, for the button when it cannot ask iOS.
    static let onKey = "vpn_on"
    /// Set by the app: stops its own core before the extension needs the lock.
    @MainActor static var beforeStart: (@MainActor () async -> Void)?

    @Parameter(title: "Uni VPN")
    var value: Bool

    @MainActor
    func perform() async throws -> some IntentResult {
        let defaults = UserDefaults(suiteName: SetVPN.appGroup)
        let setupDone = defaults?.bool(forKey: "setup_done") ?? false
        guard setupDone, let manager = try await NETunnelProviderManager.loadAllFromPreferences().first else {
            return .result()
        }
        // The same as the app's switch: on demand keeps the VPN up. Only turning on takes the
        // one VPN slot back from another VPN app; off leaves that app as it is.
        if value { manager.isEnabled = true }
        manager.isOnDemandEnabled = value
        try await manager.saveToPreferences()
        try await manager.loadFromPreferences()
        defaults?.set(value, forKey: SetVPN.onKey)
        if value {
            await SetVPN.beforeStart?()
            try manager.connection.startVPNTunnel()
        } else {
            manager.connection.stopVPNTunnel()
        }
        return .result()
    }
}
