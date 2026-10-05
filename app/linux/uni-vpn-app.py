#!/usr/bin/env python3
"""Uni VPN for Linux: the app window around the daemon's page, plus a panel icon where the
desktop shows them (Ubuntu, KDE, most others; GNOME only with the AppIndicator extension).

Role models: GNOME apps (one window per app, a second start raises it), Tailscale and Cisco
Secure Client (panel icon with the state, Connect/Disconnect, Settings, Quit).
Runs with the distribution's Python, which has PyGObject; "uni-vpn setup" picks it.
Arguments: --port N, --hidden (sign-in: panel icon only), --page P ("#settings", "&user=ab123").
"""

import json
import os
import subprocess
import sys
import threading
import time
import urllib.request

import gi

# Blank windows with some graphics drivers (NVIDIA): the page draws without DMA-BUF just as well.
os.environ.setdefault("WEBKIT_DISABLE_DMABUF_RENDERER", "1")
gi.require_version("Gtk", "3.0")
for _version in ("4.1", "4.0"):
    try:
        gi.require_version("WebKit2", _version)
        break
    except ValueError:
        continue
from gi.repository import Gio, GLib, Gtk, WebKit2  # noqa: E402

try:
    gi.require_version("AyatanaAppIndicator3", "0.1")
    from gi.repository import AyatanaAppIndicator3 as AppIndicator  # noqa: E402
except (ValueError, ImportError):
    try:
        gi.require_version("AppIndicator3", "0.1")
        from gi.repository import AppIndicator3 as AppIndicator  # noqa: E402
    except (ValueError, ImportError):
        AppIndicator = None

APP_ID = "de.davidvinu.UniVPN"
LABELS = {"connected": "Connected", "connecting": "Connecting", "disconnecting": "Disconnecting",
          "idle": "Not connected", "offline": "No network", "blocked": "Paused",
          "auth_failed": "Sign-in failed", "keyring": "Action needed", "error": "Error"}
# The loopback API needs no proxy, and the system proxy is uni-vpn's own rule.
OPENER = urllib.request.build_opener(urllib.request.ProxyHandler({}))


def option(args, name):
    return args[args.index(name) + 1] if name in args[:-1] else None


def page_from(args):
    """What to show: --page, nothing with --hidden, otherwise the window as it is."""
    page = option(args, "--page")
    if page is not None:
        return page
    return None if "--hidden" in args else ""


class App(Gtk.Application):
    def __init__(self):
        super().__init__(application_id=APP_ID, flags=Gio.ApplicationFlags.HANDLES_COMMAND_LINE)
        self.base = "http://127.0.0.1:1081/"
        self.window = None
        self.view = None
        self.indicator = None
        self.status = {}
        self.reachable = False
        self.items = {}
        self.started = False
        self.misses = 0
        self.last_start = 0.0

    # A second start (app grid, "uni-vpn app") reaches the running instance here.
    def do_command_line(self, command_line):
        args = command_line.get_arguments()[1:]
        if not self.started:
            self.started = True
            port = option(args, "--port")
            if port and port.isdigit():
                self.base = f"http://127.0.0.1:{int(port)}/"
            if AppIndicator:
                self.start_indicator()
            self.poll()
            GLib.timeout_add_seconds(3, self.poll)
        page = page_from(args)
        if page is not None:
            self.show(page)
        return 0

    def start_indicator(self):
        self.hold()  # keeps running with the window closed
        indicator = AppIndicator.Indicator.new(APP_ID, "network-vpn-disconnected-symbolic",
                                               AppIndicator.IndicatorCategory.APPLICATION_STATUS)
        indicator.set_title("Uni VPN")
        menu = Gtk.Menu()
        self.items["state"] = Gtk.MenuItem(label="")
        self.items["state"].set_sensitive(False)
        self.items["toggle"] = Gtk.MenuItem(label="Connect")
        self.items["toggle"].connect("activate", lambda _item: self.toggle())
        entries = [self.items["state"], self.items["toggle"], Gtk.SeparatorMenuItem()]
        for label, page in (("Open Uni VPN", "#main"), ("Settings", "#settings")):
            entry = Gtk.MenuItem(label=label)
            entry.connect("activate", lambda _item, p=page: self.show(p))
            entries.append(entry)
        entries.append(Gtk.SeparatorMenuItem())
        quit_item = Gtk.MenuItem(label="Quit")
        quit_item.connect("activate", lambda _item: self.quit())
        entries.append(quit_item)
        for entry in entries:
            menu.append(entry)
        menu.show_all()
        indicator.set_menu(menu)
        indicator.set_secondary_activate_target(self.items["toggle"])
        indicator.set_status(AppIndicator.IndicatorStatus.ACTIVE)
        self.indicator = indicator

    def on(self):
        state = self.status.get("state")
        return state == "connected" or (bool(self.status.get("busy")) and state != "disconnecting")

    def poll(self):
        def fetch():
            try:
                with OPENER.open(self.base + "status.json", timeout=3) as response:
                    data = json.loads(response.read().decode("utf-8"))
            except (OSError, ValueError):
                data = None
            GLib.idle_add(self.render, data)

        threading.Thread(target=fetch, daemon=True).start()
        return True

    def start_service_if_down(self):
        """Nobody should need a terminal: a stopped service is started again from here."""
        self.misses = 0 if self.reachable else self.misses + 1
        if self.misses < 2 or time.monotonic() - self.last_start < 60:
            return
        self.last_start = time.monotonic()
        try:
            subprocess.Popen(["systemctl", "--user", "start", "uni-vpn.service"], stdin=subprocess.DEVNULL,
                             stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        except OSError:
            pass

    def render(self, data):
        self.reachable = data is not None
        self.start_service_if_down()
        self.status = data or {}
        state = self.status.get("state", "")
        label = LABELS.get(state, state) if self.reachable else "Not running"
        if self.indicator:
            connected = state == "connected"
            self.indicator.set_icon_full("network-vpn-symbolic" if connected else "network-vpn-disconnected-symbolic",
                                         "Uni VPN: " + label)
            self.items["state"].set_label(label)
            self.items["toggle"].set_label("Disconnect" if self.on() else "Connect")
            self.items["toggle"].set_sensitive(self.reachable and state != "disconnecting")
        return False

    def toggle(self):
        path = "api/disconnect" if self.on() else "api/connect"
        request = urllib.request.Request(self.base + path, data=b"{}", method="POST",
                                         headers={"X-Uni-VPN": "1", "Content-Type": "application/json"})

        def send():
            try:
                OPENER.open(request, timeout=20).close()
            except OSError:
                pass
            GLib.idle_add(self.poll)

        threading.Thread(target=send, daemon=True).start()

    # --- window --------------------------------------------------------------

    def show(self, page):
        if self.window is None:
            self.window = Gtk.ApplicationWindow(application=self, title="Uni VPN")
            self.window.set_default_size(420, 660)
            self.window.set_resizable(False)
            self.window.set_position(Gtk.WindowPosition.CENTER)
            self.window.set_icon_name(APP_ID)
            self.view = WebKit2.WebView()
            settings = self.view.get_settings()
            settings.set_enable_developer_extras(False)
            settings.set_user_agent_with_application_details("UniVPN", "")
            self.view.connect("decide-policy", self.on_policy)
            self.view.connect("context-menu", lambda *_: True)
            self.view.connect("load-failed", self.on_failed)
            self.window.add(self.view)
            self.window.connect("delete-event", self.on_close)
        self.load(page)
        self.window.show_all()
        self.window.present()

    def load(self, page):
        """page: "" (as it is), "#main", "#settings", or "&user=ab123&university=ethz" from setup."""
        current = self.view.get_uri() or ""
        if current.startswith(self.base) and (page == "" or page.startswith("#")):
            if page:
                self.view.run_javascript(f"location.hash = {json.dumps(page)}", None, None, None)
            return
        self.view.load_uri(self.base + "?app=1" + page)

    def on_policy(self, _view, decision, kind):
        if kind in (WebKit2.PolicyDecisionType.NAVIGATION_ACTION, WebKit2.PolicyDecisionType.NEW_WINDOW_ACTION):
            uri = decision.get_navigation_action().get_request().get_uri()
            if not uri.startswith(self.base) and not uri.startswith("about:"):
                # Links to anything but the page itself (MFA portal) go to the default browser.
                Gio.AppInfo.launch_default_for_uri(uri, None)
                decision.ignore()
                return True
        return False

    def on_failed(self, view, _event, _uri, error):
        if error.matches(WebKit2.NetworkError.quark(), WebKit2.NetworkError.CANCELLED):
            return False  # a link that went to the browser instead
        # The service is still starting (sign-in) or restarting (update): try again shortly.

        def retry():
            if self.view is view and self.window.get_visible():
                view.load_uri(self.base + "?app=1")
            return False

        GLib.timeout_add_seconds(2, retry)
        return True

    def on_close(self, window, _event):
        if self.indicator:
            window.hide()  # the panel icon stays, like Cisco Secure Client
            return True
        self.window = self.view = None
        return False


def main():
    GLib.set_prgname(APP_ID)
    GLib.set_application_name("Uni VPN")
    return App().run(sys.argv)


if __name__ == "__main__":
    sys.exit(main())
