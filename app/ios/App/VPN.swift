import Foundation
import NetworkExtension
import Observation
import WidgetKit

/// The VPN configuration in iOS Settings and the tunnel extension's state. The home switch turns
/// connect on demand on or off: while on, iOS keeps the VPN up, also after a restart or a network
/// change (Android's Always-on VPN).
@MainActor
@Observable
final class VPN {
    static let tunnelBundle = "de.davidvinu.univpn.tunnel"
    static let controlKind = "de.davidvinu.univpn.toggle"

    private(set) var status: NEVPNStatus = .invalid
    private(set) var onDemand = false
    @ObservationIgnored private var manager: NETunnelProviderManager?
    @ObservationIgnored private var observer: NSObjectProtocol?
    @ObservationIgnored private var configObserver: NSObjectProtocol?
    /// Called on every change of `status`.
    @ObservationIgnored var onStatus: ((NEVPNStatus) -> Void)?

    /// The switch.
    var isOn: Bool { onDemand || [.connected, .connecting, .reasserting].contains(status) }
    /// The extension neither runs nor starts, so the app may run the core itself.
    var idle: Bool { status == .disconnected || status == .invalid }

    func load() async {
        let managers = (try? await NETunnelProviderManager.loadAllFromPreferences()) ?? []
        manager = managers.first
        update()
        if observer == nil {
            observer = NotificationCenter.default.addObserver(forName: .NEVPNStatusDidChange, object: nil, queue: .main) { [weak self] _ in
                Task { @MainActor in self?.update() }
            }
            // The Control Center button saves its own copy of the configuration (SetVPN): read
            // it again, as WireGuard's app does, else the switch keeps the old connect on demand.
            configObserver = NotificationCenter.default.addObserver(forName: .NEVPNConfigurationChange, object: nil, queue: .main) { [weak self] _ in
                Task { @MainActor in await self?.load() }
            }
        }
    }

    private func update() {
        let old = (status, onDemand)
        status = manager?.connection.status ?? .invalid
        onDemand = manager?.isOnDemandEnabled ?? false
        if old != (status, onDemand) {
            UserDefaults(suiteName: SetVPN.appGroup)?.set(isOn, forKey: SetVPN.onKey)
            if #available(iOS 18.0, *) {
                ControlCenter.shared.reloadControls(ofKind: VPN.controlKind)
            }
            onStatus?(status)
        }
    }

    /// Saves the configuration with on demand on. The first time iOS asks the user to allow it
    /// (the Tailscale moment); false when they did not.
    func enable() async -> Bool {
        let m = manager ?? NETunnelProviderManager()
        let proto = (m.protocolConfiguration as? NETunnelProviderProtocol) ?? NETunnelProviderProtocol()
        proto.providerBundleIdentifier = VPN.tunnelBundle
        proto.serverAddress = "Uni VPN"
        m.protocolConfiguration = proto
        m.localizedDescription = "Uni VPN"
        m.isEnabled = true
        // One rule that always connects: the core inside idles until a browser asks for a
        // university website, so there is nothing to gain from rules that wait for those
        // domains, and the VPN comes back after a restart by itself.
        m.onDemandRules = [NEOnDemandRuleConnect()]
        m.isOnDemandEnabled = true
        do {
            try await m.saveToPreferences()
            try await m.loadFromPreferences()
        } catch {
            manager = m
            update()
            return false
        }
        manager = m
        update()
        return true
    }

    func start() {
        do {
            try manager?.connection.startVPNTunnel()
        } catch {
            update()
        }
    }

    func disable() async {
        guard let m = manager else { return }
        m.isOnDemandEnabled = false
        try? await m.saveToPreferences()
        m.connection.stopVPNTunnel()
        update()
    }

    /// "Repair" while the VPN runs: the extension starts its core afresh.
    func restart() async {
        guard let m = manager else { return }
        m.connection.stopVPNTunnel()
        for _ in 0..<50 where m.connection.status != .disconnected {
            try? await Task.sleep(nanoseconds: 200_000_000)
        }
        // With on demand on, iOS may already be starting it again.
        if m.connection.status == .disconnected { try? m.connection.startVPNTunnel() }
        update()
    }

    /// The website list changed here: the extension re-applies the rule now, not in 30 s.
    func refreshRule() {
        guard status == .connected, let session = manager?.connection as? NETunnelProviderSession else { return }
        try? session.sendProviderMessage(Data("refresh".utf8)) { _ in }
    }
}
