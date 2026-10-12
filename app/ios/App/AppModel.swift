import Foundation
import NetworkExtension
import Observation
import UIKit

/// Where the app is, below Home. Setup is not a screen here: it shows whenever setup is needed
/// or asked for.
enum Screen: Hashable {
    case settings, websites, secondFactor, language, log, licenses
    case pair(String)
}

/// The answer to a POST, with the error in the user's language (nil on success).
struct PostResult {
    let reply: Reply
    let error: String?
    var ok: Bool { error == nil }
    var json: JSON? { reply.json }
}

/// The app's state, like the Android app's AppModel: the core's status, the screens, and what
/// setup has entered so far (kept here, not in the views, so nothing is lost when a view is
/// made again).
@MainActor
@Observable
final class AppModel {
    private(set) var status: JSON?
    /// The core did not answer the last poll.
    private(set) var unreachable = false
    private(set) var strings = Strings(locales: nil, setting: "", preferred: [])
    @ObservationIgnored private var locales: JSON?
    @ObservationIgnored private var stringsFor = ""

    var path: [Screen] = []
    /// Setup shows: needed, or the user changes university or username from Settings.
    var changingAccount = false
    /// Settings opens with the password dialog (the Home screen's "Update password").
    var passwordDialog = false
    /// Setup finished in this run and its last page ("All set") is still open.
    var setupFinishing = false
    var setup = SetupState()

    let vpn = VPN()
    @ObservationIgnored private var poll: Task<Void, Never>?
    @ObservationIgnored private var lastStart = Date.distantPast
    /// Turning the VPN on or off: the core changes hands, the app must not start its own.
    @ObservationIgnored private var switching = false

    init() {
        vpn.onStatus = { [weak self] status in
            guard let self else { return }
            Task { await self.vpnChanged(status) }
        }
    }

    var setupNeeded: Bool { status?.bool("setup_needed") == true }

    func t(_ key: String, _ args: [String: Any] = [:]) -> String { strings.t(key, args) }

    func open(_ screen: Screen) { path.append(screen) }

    func back() {
        if !path.isEmpty { path.removeLast() }
    }

    func home() { path = [] }

    // MARK: The core

    /// The app is on screen: run the core here unless the VPN runs it, and poll its status like
    /// the desktop apps.
    func becameActive() {
        guard poll == nil else { return }
        poll = Task { [weak self] in
            await self?.vpn.load()
            await self?.ensureCore()
            while !Task.isCancelled {
                guard let self else { return }
                await self.refresh()
                try? await Task.sleep(nanoseconds: self.unreachable ? 500_000_000 : 1_500_000_000)
            }
        }
    }

    /// iOS keeps only the extension running in the background: the app's core stops, so the
    /// extension can take the lock whenever the VPN starts.
    func wentToBackground() {
        poll?.cancel()
        poll = nil
        let app = UIApplication.shared
        let task = BackgroundTask()
        task.id = app.beginBackgroundTask { task.end() }
        Task {
            await CoreHost.shared.stop()
            task.end()
        }
    }

    /// Starts the app's core while the VPN is off. Cheap to call often.
    func ensureCore() async {
        guard vpn.idle, !switching, UIApplication.shared.applicationState != .background else { return }
        // A core that just failed to start (another one holds the lock) is not retried in a
        // tight loop.
        guard Date().timeIntervalSince(lastStart) > 1 else { return }
        lastStart = Date()
        _ = await CoreHost.shared.start(lockWaitMs: 2_000)
    }

    private func vpnChanged(_ status: NEVPNStatus) async {
        if status == .connecting {
            // The extension waits for the lock; let go of it.
            await CoreHost.shared.stop()
        } else if vpn.idle && poll != nil {
            await ensureCore()
            await refresh()
        }
    }

    /// The home switch.
    var vpnSwitch: Bool {
        get { vpn.isOn }
        set {
            Task {
                if newValue { await turnOn() } else { await turnOff() }
            }
        }
    }

    /// The switch: on asks iOS once to allow the VPN configuration (as in Tailscale).
    func turnOn() async {
        switching = true
        await CoreHost.shared.stop()
        let saved = await vpn.enable()
        if saved { vpn.start() }
        switching = false
        if !saved { await ensureCore() }
    }

    func turnOff() async {
        switching = true
        await vpn.disable()
        switching = false
    }

    /// "Repair" on a phone: start the core afresh, wherever it runs.
    func repair() async {
        if vpn.idle {
            await CoreHost.shared.stop()
            lastStart = .distantPast
            await ensureCore()
        } else {
            await vpn.restart()
        }
        await refresh()
    }

    func refresh() async {
        let reply = await API.get("/status.json")
        guard reply.ok, let json = reply.json else {
            unreachable = true
            await ensureCore()
            return
        }
        unreachable = false
        status = json
        // The Control Center button reads it without asking the core.
        UserDefaults(suiteName: CoreHost.appGroup)?.set(!json.bool("setup_needed"), forKey: "setup_done")
        let setting = json.string("language")
        let preferred = Locale.preferredLanguages
        let key = setting + "|" + preferred.joined(separator: ",")
        if locales == nil {
            locales = await API.get("/locales.json").json
        }
        if key != stringsFor, locales != nil {
            strings = Strings(locales: locales, setting: setting, preferred: preferred)
            stringsFor = key
        }
        setup.statusChanged(self)
    }

    /// POST to the core; the error text is in the user's language.
    func post(_ path: String, _ body: [String: Any]? = nil, timeout: TimeInterval = 10) async -> PostResult {
        let reply = await API.post(path, body, timeout: timeout)
        await refresh()
        return PostResult(reply: reply, error: errorText(reply))
    }

    func errorText(_ reply: Reply) -> String? {
        if reply.ok { return nil }
        if reply.status == 0 { return t("msg.not_running") }
        guard let json = reply.json else { return reply.text }
        let text = strings.message(json.string("error"), json.object("error_t"))
        return text.isEmpty ? reply.text : text
    }

    func get(_ path: String) async -> JSON? { await API.get(path).json }

    /// The website list changed: the proxy rule follows now.
    func domainsChanged() { vpn.refreshRule() }

    /// Setup is over: back to Home with a fresh setup for next time.
    func finishSetup() {
        changingAccount = false
        setupFinishing = false
        setup = SetupState()
        home()
    }
}

/// Asks iOS for a few seconds in the background, so the core stops cleanly.
@MainActor
final class BackgroundTask {
    var id = UIBackgroundTaskIdentifier.invalid

    func end() {
        guard id != .invalid else { return }
        UIApplication.shared.endBackgroundTask(id)
        id = .invalid
    }
}
