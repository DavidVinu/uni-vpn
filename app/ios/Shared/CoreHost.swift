import Foundation
import UniVPNCore
#if canImport(UIKit)
import UIKit
#endif

/// The Go core (cmd/uni-vpn-ios) in this process. The tunnel extension runs it while the VPN is
/// on, the app while the VPN is off and the app is on screen. Both use the same folders in the
/// App Group container, so they hand over through the core's lock file there.
final class CoreHost: @unchecked Sendable {
    static let shared = CoreHost()
    static let appGroup = "group.de.davidvinu.univpn"
    static let port = 1081

    enum Start: Int32 {
        case started = 0
        case busy = 1
        case failed = 2
        /// Another app listens on one of the core's ports: the core ended.
        case portsTaken = 3
    }

    // Start and Stop block (Stop until the lock and ports are free): one at a time, off the
    // main thread.
    private let queue = DispatchQueue(label: "de.davidvinu.univpn.core")

    /// The App Group container; the app's own Application Support folder in a build without the
    /// App Group (unsigned simulator tests).
    static let home: URL = {
        if let shared = FileManager.default.containerURL(forSecurityApplicationGroupIdentifier: appGroup) {
            return shared
        }
        return FileManager.default.urls(for: .applicationSupportDirectory, in: .userDomainMask)[0]
            .appendingPathComponent("uni-vpn")
    }()
    static var configHome: URL { home.appendingPathComponent("config") }
    static var stateHome: URL { home.appendingPathComponent("state") }
    static var secretsFile: URL { configHome.appendingPathComponent("uni-vpn/secrets.json") }
    /// Exists while a core of the app or the extension holds both ports (daemon.Options.ReadyFile).
    static var readyFile: URL { stateHome.appendingPathComponent("uni-vpn/api-ready") }

    /// Any app can listen on 127.0.0.1 first, and requests carry the token and the password:
    /// they go only to a core that holds its ports, like Android's Core.ready.
    static var ready: Bool { FileManager.default.fileExists(atPath: readyFile.path) }

    var running: Bool { UniVPNRunning() == 1 }

    /// Starts the core unless it runs here already; waits up to lockWaitMs for another process
    /// to let go of the lock.
    @MainActor
    func start(lockWaitMs: Int) async -> Start {
        let name = Self.deviceName()
        return await withCheckedContinuation { continuation in
            queue.async {
                continuation.resume(returning: self.startNow(lockWaitMs: lockWaitMs, deviceName: name))
            }
        }
    }

    /// Returns once the core stopped and released its lock and ports. The core bounds its own
    /// shutdown (about 5 s), so this never outlasts the time iOS gives an app going away.
    func stop() async {
        await withCheckedContinuation { (continuation: CheckedContinuation<Void, Never>) in
            stop { continuation.resume() }
        }
    }

    /// The same, queued right away from the caller's thread (no hop to a task first);
    /// `done` runs on the core's queue.
    func stop(then done: @escaping @Sendable () -> Void) {
        queue.async {
            if UniVPNRunning() == 1 { UniVPNStop() }
            done()
        }
    }

    private func startNow(lockWaitMs: Int, deviceName: String) -> Start {
        if UniVPNRunning() == 1 { return .started }
        guard let token = Secrets.value("api-token"), let key = Secrets.value("secrets-key") else {
            // The Keychain is not readable yet (before the first unlock): try again later.
            return .failed
        }
        if key.fresh {
            // A new key cannot open the old secrets: drop them, so the core asks for them again
            // instead of reporting a broken store.
            try? FileManager.default.removeItem(at: Self.secretsFile)
            // A new key also means this folder may come from another iPhone (a restored
            // backup): the code generator noted there is that phone's, which synced devices may
            // still use, so one set up here must not remove it.
            try? FileManager.default.removeItem(at: Self.stateHome.appendingPathComponent("uni-vpn/mfa-token-serial"))
        }
        let fm = FileManager.default
        try? fm.createDirectory(at: Self.configHome, withIntermediateDirectories: true)
        try? fm.createDirectory(at: Self.stateHome, withIntermediateDirectories: true)
        let args = [Self.configHome.path, Self.stateHome.path, token.hex, key.hex, deviceName, String(lockWaitMs)]
        let pointers = args.map { strdup($0) }
        defer { pointers.forEach { free($0) } }
        let code = UniVPNStart(pointers[0], pointers[1], pointers[2], pointers[3], pointers[4], pointers[5])
        return Start(rawValue: code) ?? .failed
    }

    /// The token the core expects in X-Uni-VPN-Token.
    static func token() -> String? { Secrets.value("api-token")?.hex }

    @MainActor
    private static func deviceName() -> String {
        #if canImport(UIKit)
        return UIDevice.current.name
        #else
        return "iPhone"
        #endif
    }
}
