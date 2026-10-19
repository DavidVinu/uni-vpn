import Foundation
import NetworkExtension

/// The VPN carries no traffic itself: it runs the core and hands the phone the core's proxy rule
/// (proxy.pac), so browsers send only the listed university websites to the core, which opens
/// the tunnel on demand. Everything else, the phone's DNS and the core's own connection stay on
/// the normal network. Like Android's UniVpnService.
final class PacketTunnelProvider: NEPacketTunnelProvider {
    private var pacVersion: String?
    private var poll: Task<Void, Never>?

    override func startTunnel(options: [String: NSObject]?, completionHandler: @escaping (Error?) -> Void) {
        Task { @MainActor in
            // The app stops its own core before it starts the VPN; give it time to let go.
            let started = await CoreHost.shared.start(lockWaitMs: 10_000)
            // Without the ready file the core does not hold both ports (another app listens
            // there): the proxy rule would hand that app the browsers' university traffic.
            guard started == .started, CoreHost.ready else {
                if started == .started { await CoreHost.shared.stop() }
                completionHandler(NEVPNError(.connectionFailed))
                return
            }
            // The first rule goes out even without an answer: the address answers once the core
            // listens.
            for _ in 0..<50 {
                if await API.get("/status.json").ok { break }
                try? await Task.sleep(nanoseconds: 200_000_000)
            }
            do {
                try await applyRule(force: true)
            } catch {
                await CoreHost.shared.stop()
                completionHandler(error)
                return
            }
            completionHandler(nil)
            // Websites can change on another device (syncing): the rule follows.
            poll = Task { @MainActor [weak self] in
                while !Task.isCancelled {
                    try? await Task.sleep(nanoseconds: 30_000_000_000)
                    try? await self?.applyRule(force: false)
                }
            }
        }
    }

    override func stopTunnel(with reason: NEProviderStopReason, completionHandler: @escaping () -> Void) {
        Task { @MainActor in
            poll?.cancel()
            poll = nil
            _ = await API.call("POST", "/api/disconnect", timeout: 5)
            await CoreHost.shared.stop()
            completionHandler()
        }
    }

    /// The app changed the website list.
    override func handleAppMessage(_ messageData: Data, completionHandler: ((Data?) -> Void)?) {
        Task { @MainActor in
            try? await applyRule(force: false)
            completionHandler?(nil)
        }
    }

    /// (Re)applies the settings when the rule changed, so browsers reload it.
    @MainActor
    private func applyRule(force: Bool) async throws {
        let rule = await API.get("/proxy.pac")
        // Without an answer the rule is unknown: keep it as it is.
        if !rule.ok && !force { return }
        // The rule itself: new websites or another university's security page, a new address.
        let version = Self.hash(rule.ok ? rule.text : "")
        if !force && version == pacVersion { return }

        // An address nothing uses (TEST-NET-1) and one route to its neighbour: nothing is
        // tunnelled, so the phone keeps its own DNS and every other connection as it is.
        let settings = NEPacketTunnelNetworkSettings(tunnelRemoteAddress: "192.0.2.2")
        let ipv4 = NEIPv4Settings(addresses: ["192.0.2.1"], subnetMasks: ["255.255.255.255"])
        ipv4.includedRoutes = [NEIPv4Route(destinationAddress: "192.0.2.2", subnetMask: "255.255.255.255")]
        settings.ipv4Settings = ipv4
        let proxy = NEProxySettings()
        proxy.autoProxyConfigurationEnabled = true
        proxy.proxyAutoConfigurationURL = API.pacURL(version: version)
        // The empty domain matches every host: without it a VPN that is not the default route
        // hands its proxy settings to nobody. The rule itself picks the university websites.
        proxy.matchDomains = [""]
        settings.proxySettings = proxy
        try await setTunnelNetworkSettings(settings)
        pacVersion = version
    }

    /// FNV-1a: short, and the same for the same rule in every process.
    static func hash(_ text: String) -> String {
        var h: UInt32 = 2_166_136_261
        for byte in text.utf8 {
            h ^= UInt32(byte)
            h = h &* 16_777_619
        }
        return String(h, radix: 16)
    }
}
