import re
import subprocess
import unittest
from pathlib import Path
from unittest import mock

from uni_vpn import i18n, messages, repair, tunnel
from uni_vpn import platform as pf

UI = Path(__file__).resolve().parent.parent / "uni_vpn" / "ui" / "index.html"
# Words the least technical user would not understand, and anything that reads like a command.
JARGON = re.compile(r"uni-vpn |install\.(sh|ps1)|sudo|brew |systemctl|launchctl|\bTOTP\b|SOCKS|\bPAC\b|daemon|"
                    r"openconnect|keyring|config\.toml|exit code|\blog\b", re.IGNORECASE)


class MessageTests(unittest.TestCase):
    def test_every_message_is_plain_and_names_no_command(self):
        for id, message in messages.all_messages().items():
            self.assertIsNone(JARGON.search(message), f"{id}: {message}")

    def test_ids_are_unique_and_actions_known(self):
        found = [value for value in vars(messages).values() if isinstance(value, messages.Message)]
        self.assertEqual(len(found), len({m.id for m in found}))
        for message in found:
            self.assertIn(message.action, (None, *messages.ACTIONS), message.id)

    def test_a_message_compares_like_its_text(self):
        self.assertEqual(messages.CONNECTED, "Connected")
        self.assertEqual(messages.id_of(messages.CONNECTED), "connected")
        self.assertIsNone(messages.id_of("plain text"))
        self.assertIsNone(messages.action_of("plain text"))
        with self.assertRaises(ValueError):
            messages.Message("connected", "reinstall")

    def test_tunnel_verdicts_come_from_the_list(self):
        for _needle, _state, message in tunnel.MARKERS:
            self.assertIsInstance(message, messages.Message)
        for message in tunnel.LOGIN_REJECTED.values():
            self.assertIsInstance(message, messages.Message)

    def test_the_app_names_no_command(self):
        text = UI.read_text(encoding="utf-8")
        for needle in ("uni-vpn service", "uni-vpn log", "uni-vpn password", "uni-vpn totp", "Run:"):
            self.assertNotIn(needle, text)
        self.assertIn('data-t="msg.not_running"', text)

    def test_every_app_text_is_plain(self):
        # Every language, not only the messages: labels, hints and errors too.
        for code in i18n.CODES:
            for key, text in i18n.catalog(code).items():
                if key != "settings.log":
                    self.assertIsNone(JARGON.search(text), f"{code} {key}: {text}")

    def test_every_message_has_its_text_in_every_language(self):
        for id in messages.all_messages():
            for code in i18n.CODES:
                self.assertIn("msg." + id, i18n.catalog(code), code)


class RepairCommandTests(unittest.TestCase):
    def test_linux_runs_the_installer_as_a_unit_of_its_own(self):
        with mock.patch.object(pf, "IS_WINDOWS", False), mock.patch.object(pf, "IS_MACOS", False), \
                mock.patch.object(repair.shutil, "which", return_value="/usr/bin/systemd-run"):
            command = repair.command()
        self.assertEqual(command[:2], ["/usr/bin/systemd-run", "--user"])
        self.assertIn("--wait", command)
        self.assertEqual(command[-2:], [str(pf.repo_root() / "install.sh"), "--repair"])

    def test_without_systemd_run_the_installer_runs_directly(self):
        with mock.patch.object(pf, "IS_WINDOWS", False), mock.patch.object(pf, "IS_MACOS", False), \
                mock.patch.object(repair.shutil, "which", return_value=None):
            self.assertEqual(repair.command(), ["/bin/bash", str(pf.repo_root() / "install.sh"), "--repair"])

    def test_macos_runs_the_installer_directly(self):
        with mock.patch.object(pf, "IS_WINDOWS", False), mock.patch.object(pf, "IS_MACOS", True):
            self.assertEqual(repair.command(), ["/bin/bash", str(pf.repo_root() / "install.sh"), "--repair"])

    def test_windows_runs_install_ps1(self):
        with mock.patch.object(pf, "IS_WINDOWS", True):
            command = repair.command()
        self.assertEqual(command[0], "powershell.exe")
        self.assertEqual(command[-2:], [str(pf.repo_root() / "install.ps1"), "-Repair"])

    def test_posix_start_detaches_the_installer(self):
        popen = mock.Mock()
        with mock.patch.object(pf, "IS_WINDOWS", False):
            repair.start(popen)
        kwargs = popen.call_args.kwargs
        self.assertTrue(kwargs["start_new_session"])
        self.assertEqual(kwargs["stdin"], subprocess.DEVNULL)

    def test_windows_start_leaves_the_job_and_falls_back_without(self):
        popen = mock.Mock(side_effect=[PermissionError("access denied"), "process"])
        with mock.patch.object(pf, "IS_WINDOWS", True):
            self.assertEqual(repair.start(popen), "process")
        first, second = (call.kwargs["creationflags"] for call in popen.call_args_list)
        self.assertTrue(first & repair.CREATE_BREAKAWAY_FROM_JOB)
        self.assertEqual(second, repair.CREATE_NEW_CONSOLE)


class InstallerTests(unittest.TestCase):
    ROOT = Path(__file__).resolve().parent.parent

    def test_install_sh_knows_repair(self):
        text = (self.ROOT / "install.sh").read_text(encoding="utf-8")
        self.assertIn("--repair) extra+=(--no-browser)", text)
        self.assertIn("pkexec", text)

    def test_install_ps1_knows_repair(self):
        text = (self.ROOT / "install.ps1").read_text(encoding="utf-8")
        self.assertIn("[switch]$Repair", text)
        self.assertIn('if ($Repair) { $arguments += "-Repair" }', text)


if __name__ == "__main__":
    unittest.main()
