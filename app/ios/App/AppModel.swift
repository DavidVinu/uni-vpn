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
    /// The second factor from one code, in setup or Settings.
    let auto = AutoEnroll()

    let vpn = VPN()
    @ObservationIgnored private var poll: Task<Void, Never>?
    /// The core kept running in the background for a sign-in (wentToBackground).
    @ObservationIgnored private var keepAlive: Task<Void, Never>?
    @ObservationIgnored private var lastStart = Date.distantPast
    /// Turning the VPN on or off: the core changes hands, the app must not start its own.
    @ObservationIgnored private var switching = false
    /// The VPN was just started: the app leaves the lock to the extension until then.
    @ObservationIgnored private var handOverUntil = Date.distantPast

    init() {
        // The Control Center button turns the VPN on from this process too.
        SetVPN.beforeStart = { await CoreHost.shared.stop() }
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
        // Back before the sign-in ended: it goes on in the core that kept running.
        keepAlive?.cancel()
        keepAlive = nil
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
        // Ask for background time first, then queue the stop at once: it runs while iOS still
        // lets the app run. The core gives up its lock and ports within about 5 s, well inside
        // the time iOS grants. If iOS takes the time back anyway, the handler ends the task so
        // iOS does not end the app for overrunning; the extension then finds the lock taken,
        // fails its start, and connect on demand tries again.
        let task = BackgroundTask()
        task.id = UIApplication.shared.beginBackgroundTask(withName: "stop core") { task.end() }
        keepAlive?.cancel()
        keepAlive = nil
        guard signingIn else {
            CoreHost.shared.stop {
                Task { @MainActor in task.end() }
            }
            return
        }
        // The user approves a Duo push or reads the next code in another app: the sign-in goes
        // on for as long as iOS lets the app run, and the core still stops in time (iOS ends an
        // app that is suspended holding the lock in the shared container). Stopped anyway, the
        // one-code setup asks for a new code on return (AutoEnroll.progress).
        keepAlive = Task { [weak self] in
            while !Task.isCancelled, self?.signingIn == true, UIApplication.shared.backgroundTimeRemaining > 10 {
                try? await Task.sleep(nanoseconds: 1_000_000_000)
                await self?.refresh()
            }
            if Task.isCancelled {
                task.end()
                return
            }
            CoreHost.shared.stop {
                Task { @MainActor in task.end() }
            }
        }
    }

    /// A sign-in the user waits for runs in the app's core (the VPN's core runs on by itself):
    /// setup's first one, or the one-code setup.
    private var signingIn: Bool {
        vpn.idle && (setup.sending || (setup.step == .connecting && setup.sentAt != nil) || auto.sending || auto.running)
    }

    /// Starts the app's core while the VPN is off. Cheap to call often.
    func ensureCore() async {
        guard vpn.idle, !switching, Date() >= handOverUntil,
              UIApplication.shared.applicationState != .background else { return }
        // A core that just failed to start (another one holds the lock) is not retried in a
        // tight loop.
        guard Date().timeIntervalSince(lastStart) > 1 else { return }
        lastStart = Date()
        _ = await CoreHost.shared.start(lockWaitMs: 2_000)
    }

    private func vpnChanged(_ status: NEVPNStatus) async {
        if status == .connecting || status == .connected {
            // The extension has it now; from here on the status says who runs the core.
            handOverUntil = .distantPast
        }
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
        if saved {
            // The status still reads "disconnected" for a moment: until it moves on (or the
            // start failed for 15 s), the lock is the extension's, not the app's.
            handOverUntil = Date().addingTimeInterval(15)
            vpn.start()
        }
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
        auto.progress(self)
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
        auto.cancel()
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
