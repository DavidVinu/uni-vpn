"""Checks against the real Windows APIs. They only run on Windows (CI: windows-latest)."""
import os
import shutil
import subprocess
import sys
import tempfile
import time
import unittest
import uuid
from pathlib import Path

from uni_vpn import windows, wintunnel

ROOT = Path(__file__).resolve().parent.parent
VPNC = ROOT / "bin" / "uni-vpn-vpnc.js"


@unittest.skipUnless(sys.platform == "win32", "Windows only")
class CredentialManagerTests(unittest.TestCase):
    def test_write_read_delete(self):
        target = windows.credential_target("uni-vpn-test", uuid.uuid4().hex)
        self.addCleanup(windows.cred_delete, target)
        self.assertIsNone(windows.cred_read(target))
        windows.cred_write(target, "ab123", "pä$$ w0rd".encode())
        self.assertEqual(windows.cred_read(target), "pä$$ w0rd".encode())
        windows.cred_write(target, "ab123", b"second")
        self.assertEqual(windows.cred_read(target), b"second")
        self.assertTrue(windows.cred_delete(target))
        self.assertIsNone(windows.cred_read(target))
        self.assertFalse(windows.cred_delete(target))


@unittest.skipUnless(sys.platform == "win32", "Windows only")
class RegistryTests(unittest.TestCase):
    def test_proxy_roundtrip(self):
        before = windows.proxy_get()["url"]
        self.addCleanup(windows.proxy_set, before)
        windows.proxy_set("http://127.0.0.1:1081/proxy.pac")
        self.assertEqual(windows.proxy_get()["url"], "http://127.0.0.1:1081/proxy.pac")
        windows.proxy_set("")
        self.assertEqual(windows.proxy_get()["url"], "")

    def test_user_path_roundtrip(self):
        directory = str(Path(tempfile.gettempdir()) / f"uni-vpn-test-{uuid.uuid4().hex}")
        self.addCleanup(windows.remove_user_path, directory)
        self.assertTrue(windows.add_user_path(directory))
        self.assertFalse(windows.add_user_path(directory + "\\"))
        windows.remove_user_path(directory)
        self.assertTrue(windows.add_user_path(directory))


@unittest.skipUnless(sys.platform == "win32", "Windows only")
class ProcessTests(unittest.TestCase):
    def test_system_dirs_come_from_the_api(self):
        system, windows_dir = windows.system_dirs()
        self.assertTrue(os.path.isfile(os.path.join(system, "netsh.exe")))
        self.assertTrue(os.path.isdir(windows_dir))

    def test_ctrl_c_reaches_a_process_with_its_own_console(self):
        script = "import time\ntry:\n    time.sleep(30)\nexcept KeyboardInterrupt:\n    raise SystemExit(7)\n"
        proc = subprocess.Popen([sys.executable, "-c", script], creationflags=windows.CREATE_NEW_CONSOLE,
                                startupinfo=windows.hidden_console_startupinfo())
        self.addCleanup(proc.kill)
        time.sleep(1)  # until Python has installed its Ctrl+C handler
        self.assertTrue(windows.send_ctrl_c(proc.pid))
        self.assertEqual(proc.wait(10), 7)

    def test_awake_clock_advances(self):
        first = windows.awake_seconds()
        time.sleep(0.2)
        self.assertGreater(windows.awake_seconds() - first, 0.1)

    def test_ctrl_c_still_reaches_children_after_allowing_it(self):
        windows.allow_ctrl_c_for_children()
        self.test_ctrl_c_reaches_a_process_with_its_own_console()

    def test_task_xml_is_accepted_by_task_scheduler(self):
        if not windows.is_admin():
            self.skipTest("creating a task with the highest run level needs administrator rights")
        name = f"uni-vpn-test-{uuid.uuid4().hex[:8]}"
        xml_file = Path(tempfile.mkdtemp()) / "task.xml"
        xml = windows.render_task(windows.pythonw(sys.executable), str(ROOT / "bin" / "uni-vpn"),
                                  windows.current_user(), str(ROOT))
        xml_file.write_text(xml, encoding="utf-16")
        created = subprocess.run(["schtasks", "/Create", "/TN", name, "/XML", str(xml_file), "/F"],
                                 capture_output=True, text=True)
        self.addCleanup(subprocess.run, ["schtasks", "/Delete", "/TN", name, "/F"], capture_output=True)
        self.assertEqual(created.returncode, 0, created.stdout + created.stderr)
        query = subprocess.run(["schtasks", "/Query", "/TN", name, "/XML"], capture_output=True, text=True)
        self.assertIn("HighestAvailable", query.stdout)


@unittest.skipUnless(sys.platform == "win32" and shutil.which("cscript"), "Windows Script Host only")
class VpncScriptTests(unittest.TestCase):
    def run_script(self, reason, **extra):
        tmp = Path(tempfile.mkdtemp())
        state = tmp / "tunnel-1080.env"
        env = dict(os.environ, UNI_VPN_DRY="1", UNI_VPN_STATE=str(state), reason=reason, **extra)
        result = subprocess.run(["cscript", "//nologo", "//E:JScript", str(VPNC)], env=env,
                                capture_output=True, text=True, timeout=30)
        return result, state

    def test_connect_configures_adapter_and_writes_state(self):
        result, state = self.run_script("connect", TUNIDX="42", INTERNAL_IP4_ADDRESS="10.8.0.5",
                                        INTERNAL_IP4_NETMASK="255.255.255.0", INTERNAL_IP4_MTU="1290",
                                        INTERNAL_IP4_DNS="10.0.0.1 10.0.0.2")
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        lines = [line for line in result.stdout.splitlines() if line.startswith("RUN ")]
        self.assertEqual(lines, [
            "RUN netsh interface ipv4 set subinterface 42 mtu=1290 store=active",
            "RUN netsh interface ipv4 set interface 42 metric=9000 store=active",
            "RUN netsh interface ipv4 set interface 42 dadtransmits=0 store=active",
            "RUN netsh interface ipv4 set address 42 static 10.8.0.5 255.255.255.0 store=active",
            "RUN netsh interface ipv4 delete dnsservers 42 all",
            "RUN netsh interface ipv4 delete route 0.0.0.0/0 42 store=active",
            "RUN netsh interface ipv4 add route 0.0.0.0/0 42 metric=9000 store=active",
        ])
        values = wintunnel.parse_state(state.read_text())
        self.assertNotIn("ERROR", values)
        self.assertEqual(values["INTERNAL_IP4_ADDRESS"], "10.8.0.5")
        self.assertEqual(wintunnel.dns_servers(values), ["10.0.0.1", "10.0.0.2"])

    def test_connect_without_address_reports_error(self):
        result, state = self.run_script("connect", TUNIDX="42", INTERNAL_IP4_ADDRESS="")
        self.assertEqual(result.returncode, 1)
        self.assertIn("no interface index", wintunnel.parse_state(state.read_text())["ERROR"])

    def test_failed_route_is_reported_in_the_state_file(self):
        result, state = self.run_script("connect", TUNIDX="42", INTERNAL_IP4_ADDRESS="10.8.0.5",
                                        UNI_VPN_DRY_FAIL="add route")
        self.assertEqual(result.returncode, 1)
        self.assertIn("add route 0.0.0.0/0 42", wintunnel.parse_state(state.read_text())["ERROR"])

    def test_reconnect_tolerates_the_address_being_set_already(self):
        result, state = self.run_script("reconnect", TUNIDX="42", INTERNAL_IP4_ADDRESS="10.8.0.5",
                                        UNI_VPN_DRY_FAIL="set address")
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertNotIn("ERROR", wintunnel.parse_state(state.read_text()))

    def test_real_netsh_failure_on_a_missing_interface_is_reported(self):
        tmp = Path(tempfile.mkdtemp())
        state = tmp / "tunnel-1080.env"
        env = windows.trusted_env(dict(os.environ), *windows.system_dirs())
        env.update(UNI_VPN_STATE=str(state), reason="connect", TUNIDX="99999", INTERNAL_IP4_ADDRESS="10.8.0.5",
                   ComSpec="C:\\nonexistent\\evil.exe")
        result = subprocess.run(["cscript", "//nologo", "//E:JScript", str(VPNC)], env=env,
                                capture_output=True, text=True, timeout=60)
        self.assertEqual(result.returncode, 1, result.stdout + result.stderr)
        self.assertIn("set address 99999", wintunnel.parse_state(state.read_text())["ERROR"])

    def test_disconnect_removes_state(self):
        _, state = self.run_script("connect", TUNIDX="1", INTERNAL_IP4_ADDRESS="10.8.0.5")
        self.assertTrue(state.exists())
        result = subprocess.run(["cscript", "//nologo", "//E:JScript", str(VPNC)],
                                env=dict(os.environ, UNI_VPN_STATE=str(state), reason="disconnect"),
                                capture_output=True, text=True, timeout=30)
        self.assertEqual(result.returncode, 0)
        self.assertFalse(state.exists())


if __name__ == "__main__":
    unittest.main()
