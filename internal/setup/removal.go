package setup

import (
	"errors"
	"fmt"
	"os"

	"github.com/DavidVinu/uni-vpn/internal/service"
)

// Removing the app removes uni-vpn. Dragging "Uni VPN" to the Trash (macOS) or removing the
// package in the App Center (Linux) runs nothing of ours, and each user's service would keep
// running from the user's own copy. So the service watches the installer's folder (see
// internal/daemon/removal.go) and, once it is gone, calls Remove. It mirrors
// uni_vpn/removal.py; the keyring entries stay, like the data of any app moved to the Trash.

// forget unregisters the service without stopping it: stopping ends the running daemon.
type forget struct{ s *Setup }

func (f forget) Install(bool) ([]string, error) { return nil, errors.New("not available") }
func (f forget) UnitTargetPath() string         { return f.s.Service.UnitTargetPath() }

func (f forget) Uninstall() (bool, error) {
	s := f.s
	if !s.macOS() {
		s.Run(service.Cmd{Args: []string{"systemctl", "--user", "disable", service.UnitName}, Capture: true})
	}
	if err := os.Remove(f.UnitTargetPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	if !s.macOS() {
		s.Run(service.Cmd{Args: []string{"systemctl", "--user", "daemon-reload"}, Capture: true})
	}
	return true, nil
}

// Remove does everything "uni-vpn uninstall" does, keeping the keyring entries; the service
// ends last.
func (s *Setup) Remove() int {
	quiet := *s
	quiet.Service = forget{s}
	quiet.Input = func(string) (string, error) { return "n", nil }
	code, err := quiet.Uninstall(Args{})
	if err != nil {
		fmt.Fprintln(s.Out, err)
		code = 1
	}
	if s.macOS() {
		s.Run(service.Cmd{Args: []string{"launchctl", "bootout", fmt.Sprintf("gui/%d/%s", s.UID, service.Label)}, Capture: true})
	} else {
		s.Run(service.Cmd{Args: []string{"systemctl", "--user", "stop", service.UnitName}, Capture: true})
	}
	return code
}
