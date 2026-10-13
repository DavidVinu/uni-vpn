// Uni VPN for macOS: menu bar item plus the app window around the daemon's page.
// Role models: Tailscale (menu bar item that shows the state, Dock icon only while the
// window is open), Cisco Secure Client (small fixed window), macOS System Settings (Cmd+,).
// Built by "uni-vpn setup" with swiftc; the port comes from Info.plist (UniVPNPort).

import Cocoa
import UniformTypeIdentifiers
import WebKit

let port = (Bundle.main.object(forInfoDictionaryKey: "UniVPNPort") as? String).flatMap { Int($0) } ?? 1081
let base = URL(string: "http://127.0.0.1:\(port)/")!
let windowSize = NSSize(width: 420, height: 660)
let showNote = Notification.Name("de.davidvinu.uni-vpn.app.show")
// Where the automatic updates put the new app (internal/desktop): /Applications belongs to root.
let updatedApp = NSHomeDirectory() + "/Library/Application Support/uni-vpn/app/desktop/Uni VPN.app"

func argument(_ name: String) -> String? {
    let args = CommandLine.arguments
    guard let i = args.firstIndex(of: name), i + 1 < args.count else { return nil }
    return args[i + 1]
}

func resolved(_ path: String) -> String { URL(fileURLWithPath: path).resolvingSymlinksInPath().standardizedFileURL.path }

// The app that should run: the updated one once there is one.
func currentApp() -> String {
    resolved(FileManager.default.fileExists(atPath: updatedApp + "/Contents/MacOS/Uni VPN") ? updatedApp : Bundle.main.bundlePath)
}

// Identifies the program file of app, which an update replaces.
func stamp(_ app: String) -> String? {
    guard let a = try? FileManager.default.attributesOfItem(atPath: app + "/Contents/MacOS/Uni VPN") else { return nil }
    return "\(a[.systemFileNumber] ?? "")|\(a[.size] ?? "")|\((a[.modificationDate] as? Date)?.timeIntervalSince1970 ?? 0)"
}

struct Status {
    var state = "unknown"
    var busy = false
    var reachable = false
    var menu: [String: String] = [:]
    var on: Bool { state == "connected" || (busy && state != "disconnecting") }
}

// English until the service answers with the menus in the user's language (status.json?menu=).
var texts: [String: String] = [
    "look.connected": "Connected", "look.connecting": "Connecting", "look.disconnecting": "Disconnecting",
    "look.idle": "Not connected", "look.offline": "No network", "look.blocked": "Paused",
    "look.auth_failed": "Sign-in failed", "look.keyring": "Action needed", "look.error": "Error",
    "menu.not_running": "Not running", "menu.connect": "Connect", "menu.disconnect": "Disconnect",
    "menu.open": "Open Uni VPN", "menu.settings": "Settings", "menu.quit": "Quit Uni VPN",
    "menu.about": "About Uni VPN", "menu.hide": "Hide Uni VPN", "menu.edit": "Edit", "menu.undo": "Undo",
    "menu.redo": "Redo", "menu.cut": "Cut", "menu.copy": "Copy", "menu.paste": "Paste",
    "menu.select_all": "Select All", "menu.window": "Window", "menu.minimize": "Minimize", "menu.close": "Close",
]
func t(_ key: String) -> String { texts[key] ?? key }

final class AppDelegate: NSObject, NSApplicationDelegate, NSWindowDelegate, WKNavigationDelegate, WKUIDelegate, NSMenuDelegate {
    var window: NSWindow?
    var webView: WKWebView?
    // The page last asked for: a load that fails (service starting) is tried again as that page.
    var target = URL(string: "?app=1", relativeTo: base)!.absoluteURL
    let statusItem = NSStatusBar.system.statusItem(withLength: NSStatusItem.squareLength)
    let menu = NSMenu()
    let stateItem = NSMenuItem(title: "", action: nil, keyEquivalent: "")
    let toggleItem = NSMenuItem(title: t("menu.connect"), action: #selector(AppDelegate.toggle), keyEquivalent: "")
    var status = Status()
    var pending: String?
    var launched = false
    var misses = 0
    var lastStart = Date.distantPast
    var program: String?
    var seen: String?
    var restarting = false
    var setupStarted = false

    func applicationDidFinishLaunching(_ note: Notification) {
        // Started by the copy it replaces after an update: that one quits first.
        let after = argument("--after").flatMap { pid_t($0) }
        if let after = after {
            for _ in 0..<100 where kill(after, 0) == 0 { usleep(100_000) }
        }
        // One menu bar item: the login item and the installer's "open" can start a second copy
        // at the same moment. The newer one hands its page to the older one and quits. This copy
        // listens before it decides, so that a page handed over meanwhile is not lost.
        DistributedNotificationCenter.default().addObserver(forName: showNote, object: nil, queue: .main) { [weak self] note in
            self?.show(note.object as? String ?? "")
        }
        let me = NSRunningApplication.current
        let older = NSRunningApplication.runningApplications(withBundleIdentifier: Bundle.main.bundleIdentifier ?? "")
            .filter { $0 != me && !$0.isTerminated && $0.processIdentifier != after }
            .first { other in
                // The login item has no launchDate (launchd starts it, not LaunchServices): then the
                // process id decides, the same way in both copies, so that exactly one stays.
                guard let a = other.launchDate, let b = me.launchDate, a != b else {
                    return other.processIdentifier < me.processIdentifier
                }
                return a < b
            }
        if let older = older {
            if !CommandLine.arguments.contains("--hidden") || pending != nil {
                DistributedNotificationCenter.default().postNotificationName(
                    showNote, object: pending ?? "", userInfo: nil, deliverImmediately: true)
                older.activate(options: [])
            }
            exit(0)
        }
        buildMenus()
        stateItem.isEnabled = false
        toggleItem.target = self
        menu.delegate = self
        statusItem.menu = menu
        render()
        poll()
        Timer.scheduledTimer(withTimeInterval: 3, repeats: true) { [weak self] _ in self?.poll() }
        launched = true
        program = stamp(Bundle.main.bundlePath)
        if !CommandLine.arguments.contains("--hidden") || pending != nil {
            show(pending ?? "")
        }
    }

    func item(_ title: String, _ action: Selector, _ key: String) -> NSMenuItem {
        let entry = NSMenuItem(title: title, action: action, keyEquivalent: key)
        entry.target = self
        return entry
    }

    // The menu bar item's menu and the app's main menu, again whenever the language changes.
    // Without an Edit menu, Cmd+C/V/X/A do nothing in the page's text fields.
    func buildMenus() {
        menu.removeAllItems()
        menu.addItem(stateItem)
        menu.addItem(toggleItem)
        menu.addItem(.separator())
        menu.addItem(item(t("menu.open"), #selector(openMain), ""))
        menu.addItem(item(t("menu.settings") + "…", #selector(openSettings), ","))
        menu.addItem(.separator())
        menu.addItem(item(t("menu.quit"), #selector(quit), "q"))
        let main = NSMenu()
        let appMenu = NSMenu()
        appMenu.addItem(withTitle: t("menu.about"), action: #selector(NSApplication.orderFrontStandardAboutPanel(_:)), keyEquivalent: "")
        appMenu.addItem(.separator())
        appMenu.addItem(item(t("menu.settings") + "…", #selector(openSettings), ","))
        appMenu.addItem(.separator())
        appMenu.addItem(withTitle: t("menu.hide"), action: #selector(NSApplication.hide(_:)), keyEquivalent: "h")
        appMenu.addItem(.separator())
        appMenu.addItem(item(t("menu.quit"), #selector(quit), "q"))
        let editMenu = NSMenu(title: t("menu.edit"))
        editMenu.addItem(withTitle: t("menu.undo"), action: Selector(("undo:")), keyEquivalent: "z")
        editMenu.addItem(withTitle: t("menu.redo"), action: Selector(("redo:")), keyEquivalent: "Z")
        editMenu.addItem(.separator())
        editMenu.addItem(withTitle: t("menu.cut"), action: #selector(NSText.cut(_:)), keyEquivalent: "x")
        editMenu.addItem(withTitle: t("menu.copy"), action: #selector(NSText.copy(_:)), keyEquivalent: "c")
        editMenu.addItem(withTitle: t("menu.paste"), action: #selector(NSText.paste(_:)), keyEquivalent: "v")
        editMenu.addItem(withTitle: t("menu.select_all"), action: #selector(NSText.selectAll(_:)), keyEquivalent: "a")
        let windowMenu = NSMenu(title: t("menu.window"))
        windowMenu.addItem(withTitle: t("menu.minimize"), action: #selector(NSWindow.performMiniaturize(_:)), keyEquivalent: "m")
        windowMenu.addItem(withTitle: t("menu.close"), action: #selector(NSWindow.performClose(_:)), keyEquivalent: "w")
        for sub in [appMenu, editMenu, windowMenu] {
            let holder = NSMenuItem()
            holder.submenu = sub
            main.addItem(holder)
        }
        NSApp.mainMenu = main
        NSApp.windowsMenu = windowMenu
    }

    // --- window ---------------------------------------------------------------

    func show(_ page: String) {
        if window == nil {
            let config = WKWebViewConfiguration()
            config.applicationNameForUserAgent = "UniVPN"
            let view = WKWebView(frame: NSRect(origin: .zero, size: windowSize), configuration: config)
            view.navigationDelegate = self
            view.uiDelegate = self
            let win = NSWindow(contentRect: NSRect(origin: .zero, size: windowSize),
                               styleMask: [.titled, .closable, .miniaturizable], backing: .buffered, defer: false)
            win.title = "Uni VPN"
            win.contentView = view
            win.isReleasedWhenClosed = false
            win.delegate = self
            win.center()
            win.setFrameAutosaveName("UniVPN")
            window = win
            webView = view
        }
        load(page)
        // A Dock icon while the window is open, like Tailscale.
        NSApp.setActivationPolicy(.regular)
        window?.makeKeyAndOrderFront(nil)
        NSApp.activate(ignoringOtherApps: true)
    }

    // page: "" (as it is), "#main", "#settings", or "&user=ab123&university=ethz" from setup.
    func load(_ page: String) {
        guard let view = webView else { return }
        if let current = view.url, current.host == base.host, current.port == base.port, page.isEmpty || page.hasPrefix("#") {
            if !page.isEmpty { view.evaluateJavaScript("location.hash = '\(page)'") }
            return
        }
        target = URL(string: "?app=1" + page, relativeTo: base)!.absoluteURL
        view.load(URLRequest(url: target))
    }

    func windowWillClose(_ note: Notification) {
        NSApp.setActivationPolicy(.accessory)
    }

    func applicationShouldHandleReopen(_ app: NSApplication, hasVisibleWindows: Bool) -> Bool {
        show("")
        return true
    }

    func applicationShouldTerminateAfterLastWindowClosed(_ app: NSApplication) -> Bool { false }

    // uni-vpn://settings, uni-vpn://open?user=ab123 (from "uni-vpn app" and the installer), and
    // otpauth:// links (the MFA portal's "follow this link"), whose key the assistant reads.
    func application(_ app: NSApplication, open urls: [URL]) {
        for url in urls {
            let page: String
            switch url.scheme?.lowercased() {
            case "uni-vpn":
                page = url.host == "settings" ? "#settings" : (url.query.map { "&" + $0 } ?? "")
            case "otpauth":
                page = "#otpauth=" + (url.absoluteString.addingPercentEncoding(withAllowedCharacters: .alphanumerics) ?? "")
            default:
                continue
            }
            if launched { show(page) } else { pending = page }
        }
    }

    // Links to anything but the page itself (MFA portal) open in the default browser.
    @objc(webView:decidePolicyForNavigationAction:decisionHandler:)
    func webView(_ view: WKWebView, decidePolicyFor action: WKNavigationAction, decisionHandler: @escaping (WKNavigationActionPolicy) -> Void) {
        if let url = action.request.url, url.host != "127.0.0.1" && url.host != "localhost" {
            // Only web links: another scheme (a file, an app's own) would run without the browser's question.
            if ["http", "https"].contains(url.scheme?.lowercased() ?? "") { NSWorkspace.shared.open(url) }
            decisionHandler(.cancel)
            return
        }
        decisionHandler(.allow)
    }

    @objc(webView:createWebViewWithConfiguration:forNavigationAction:windowFeatures:)
    func webView(_ view: WKWebView, createWebViewWith config: WKWebViewConfiguration, for action: WKNavigationAction, windowFeatures: WKWindowFeatures) -> WKWebView? {
        if let url = action.request.url { NSWorkspace.shared.open(url) }
        return nil
    }

    // "Choose picture" in the sign-in code guide: WKWebView shows no file picker by itself.
    @objc(webView:runOpenPanelWithParameters:initiatedByFrame:completionHandler:)
    func webView(_ view: WKWebView, runOpenPanelWith parameters: WKOpenPanelParameters, initiatedByFrame frame: WKFrameInfo, completionHandler: @escaping ([URL]?) -> Void) {
        let panel = NSOpenPanel()
        panel.canChooseFiles = true
        panel.canChooseDirectories = false
        panel.allowsMultipleSelection = parameters.allowsMultipleSelection
        panel.allowedContentTypes = [.image]
        panel.directoryURL = FileManager.default.urls(for: .desktopDirectory, in: .userDomainMask).first
        if let win = window {
            panel.beginSheetModal(for: win) { completionHandler($0 == .OK ? panel.urls : nil) }
        } else {
            panel.begin { completionHandler($0 == .OK ? panel.urls : nil) }
        }
    }

    // The daemon is still starting (login) or restarting (update): try again shortly.
    @objc(webView:didFailProvisionalNavigation:withError:)
    func webView(_ view: WKWebView, didFailProvisionalNavigation nav: WKNavigation!, withError error: Error) {
        DispatchQueue.main.asyncAfter(deadline: .now() + 2) { [weak self] in
            guard let self = self, let win = self.window, win.isVisible else { return }
            view.load(URLRequest(url: self.target))  // the page asked for (Settings), not the main one
        }
    }

    // --- menu bar -------------------------------------------------------------

    func menuWillOpen(_ menu: NSMenu) { poll() }

    func render() {
        let symbol = status.state == "connected" ? "shield.fill" : "shield"
        let image = NSImage(systemSymbolName: symbol, accessibilityDescription: "Uni VPN")
        image?.isTemplate = true
        statusItem.button?.image = image
        statusItem.button?.appearsDisabled = !status.reachable
        if status.menu.contains(where: { texts[$0.key] != $0.value }) {
            texts.merge(status.menu) { _, new in new }
            buildMenus()
        }
        let label = status.reachable ? texts["look." + status.state] ?? status.state : t("menu.not_running")
        statusItem.button?.toolTip = "Uni VPN: " + label
        stateItem.title = label
        toggleItem.title = t(status.on ? "menu.disconnect" : "menu.connect")
        toggleItem.isEnabled = status.reachable && status.state != "disconnecting"
    }

    func poll() {
        let languages = Locale.preferredLanguages.joined(separator: ",")
        var parts = URLComponents(url: base.appendingPathComponent("status.json"), resolvingAgainstBaseURL: false)!
        // shell=1: this app restarts itself after updates (restartIfUpdated).
        parts.queryItems = [URLQueryItem(name: "menu", value: languages), URLQueryItem(name: "shell", value: "1")]
        var request = URLRequest(url: parts.url!)
        request.cachePolicy = .reloadIgnoringLocalCacheData
        request.timeoutInterval = 3
        URLSession.shared.dataTask(with: request) { [weak self] data, response, _ in
            var next = Status()
            // Only a 200: the uni-vpn of another account on this Mac answers 403 (with JSON).
            if (response as? HTTPURLResponse)?.statusCode == 200, let data = data,
               let json = (try? JSONSerialization.jsonObject(with: data)) as? [String: Any] {
                next.state = json["state"] as? String ?? "unknown"
                next.busy = json["busy"] as? Bool ?? false
                next.menu = json["menu"] as? [String: String] ?? [:]
                next.reachable = true
            }
            DispatchQueue.main.async {
                self?.status = next
                self?.render()
                self?.startServiceIfDown()
                self?.restartIfUpdated()
            }
        }.resume()
    }

    // After an update the new version takes over without a window, once this one's window is
    // closed (Chrome, Tailscale). The same file twice in a row: the update is complete.
    func restartIfUpdated() {
        let app = currentApp()
        let now = stamp(app)
        let me = resolved(Bundle.main.bundlePath)
        let changed = now != nil && (app != me || now != program) && now == seen
        seen = now
        // A hidden app (Cmd+H) reports its window as not visible: it is still open.
        guard changed, !restarting, !(window?.isVisible ?? false), !(window?.isMiniaturized ?? false), !NSApp.isHidden else { return }
        restarting = true
        let config = NSWorkspace.OpenConfiguration()
        config.createsNewApplicationInstance = true
        config.activates = false
        config.arguments = ["--hidden", "--after", String(getpid())]
        NSWorkspace.shared.openApplication(at: URL(fileURLWithPath: app), configuration: config) { started, _ in
            DispatchQueue.main.async {
                if started != nil { exit(0) }
                // Stays as it is; tried again after the next update.
                self.program = now
                self.restarting = app != me
            }
        }
    }

    // Nobody should need a terminal: a stopped service is started again from here.
    func startServiceIfDown() {
        misses = status.reachable ? 0 : misses + 1
        guard misses >= 2, Date().timeIntervalSince(lastStart) > 60 else { return }
        lastStart = Date()
        let domain = "gui/\(getuid())"
        let plist = NSHomeDirectory() + "/Library/LaunchAgents/de.davidvinu.uni-vpn.plist"
        if setUpIfMissing(plist) { return }
        for args in [["bootstrap", domain, plist], ["kickstart", domain + "/de.davidvinu.uni-vpn"]] {
            let task = Process()
            task.executableURL = URL(fileURLWithPath: "/bin/launchctl")
            task.arguments = args
            task.standardOutput = FileHandle.nullDevice
            task.standardError = FileHandle.nullDevice
            try? task.run()
            task.waitUntilExit()
        }
    }

    // Not set up for this user: the installer only sets up whoever is signed in at the time
    // (not nobody, not another account on this Mac). The package's uni-vpn-open does it, once
    // per launch; it shows its own message if it fails.
    func setUpIfMissing(_ plist: String) -> Bool {
        let files = FileManager.default
        let core = NSHomeDirectory() + "/Library/Application Support/uni-vpn/app/bin/uni-vpn-core"
        guard !files.fileExists(atPath: plist) || !files.isExecutableFile(atPath: core) else { return false }
        if setupStarted { return true }
        // The updated app next to the core has no uni-vpn-open; the installed one does.
        let opener = [Bundle.main.bundlePath, "/Applications/Uni VPN.app"]
            .map { $0 + "/Contents/Resources/uni-vpn-open" }
            .first { files.isExecutableFile(atPath: $0) }
        guard let path = opener else { return false }
        setupStarted = true
        DispatchQueue.global().async {
            let task = Process()
            task.executableURL = URL(fileURLWithPath: path)
            task.standardOutput = FileHandle.nullDevice
            task.standardError = FileHandle.nullDevice
            try? task.run()
            task.waitUntilExit()
        }
        return true
    }

    @objc func toggle() {
        var request = URLRequest(url: base.appendingPathComponent(status.on ? "api/disconnect" : "api/connect"))
        request.httpMethod = "POST"
        request.setValue("1", forHTTPHeaderField: "X-Uni-VPN")
        request.setValue("application/json", forHTTPHeaderField: "Content-Type")
        request.httpBody = Data("{}".utf8)
        URLSession.shared.dataTask(with: request) { [weak self] _, _, _ in
            DispatchQueue.main.async { self?.poll() }
        }.resume()
    }

    @objc func openMain() { show("#main") }
    @objc func openSettings() { show("#settings") }
    @objc func quit() { NSApp.terminate(nil) }
}

let app = NSApplication.shared
let delegate = AppDelegate()
app.delegate = delegate
app.setActivationPolicy(.accessory)
app.run()
