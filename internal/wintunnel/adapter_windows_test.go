//go:build windows

package wintunnel

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// The adapter test needs administrator rights and Windows on ARM, so it runs only where CI sets
// these (ci.yml, windows-arm-install): the x64 wintun.dll openconnect ships, and this package's
// tests built for x64, which Windows emulates like openconnect.
const (
	wintunDLLEnv  = "UNI_VPN_WINTUN_DLL"
	x64TestEnv    = "UNI_VPN_X64_TEST"
	helperModeEnv = "UNI_VPN_WINTUN_HELPER"
	testAdapter   = "uni-vpn-ci"
)

// TestWintunHelper is the emulated side: it does what openconnect does with Wintun.
func TestWintunHelper(t *testing.T) {
	mode := os.Getenv(helperModeEnv)
	if mode == "" {
		t.Skip("helper for TestLeftoverAdapterOfEmulatedWintunIsRemoved")
	}
	dll := windows.NewLazyDLL(os.Getenv(wintunDLLEnv))
	logger := syscall.NewCallback(func(level uint32, _ uint64, message *uint16) uintptr {
		fmt.Printf("wintun %d: %s\n", level, windows.UTF16PtrToString(message))
		return 0
	})
	dll.NewProc("WintunSetLogger").Call(logger)
	name, _ := windows.UTF16PtrFromString(testAdapter)
	kind, _ := windows.UTF16PtrFromString("OpenConnect")
	create := func() uintptr {
		adapter, _, err := dll.NewProc("WintunCreateAdapter").Call(uintptr(unsafe.Pointer(name)), uintptr(unsafe.Pointer(kind)), 0)
		if adapter == 0 {
			t.Fatalf("WintunCreateAdapter: %v", err)
		}
		return adapter
	}
	open := func() uintptr {
		adapter, _, _ := dll.NewProc("WintunOpenAdapter").Call(uintptr(unsafe.Pointer(name)))
		return adapter
	}
	closeAdapter := func(adapter uintptr) { dll.NewProc("WintunCloseAdapter").Call(adapter) }
	switch mode {
	case "create-close": // one connection that ends
		closeAdapter(create())
	case "open": // the next connection, while the name is taken
		if adapter := open(); adapter != 0 {
			closeAdapter(adapter)
			t.Fatal("opened")
		}
	case "create-open": // a connection on a free name: the adapter is there and opens
		adapter := create()
		defer closeAdapter(adapter)
		if again := open(); again == 0 {
			t.Fatal("WintunOpenAdapter failed")
		} else {
			closeAdapter(again)
		}
	default:
		t.Fatal(mode)
	}
}

func runHelper(t *testing.T, mode string) error {
	cmd := exec.Command(os.Getenv(x64TestEnv), "-test.run=^TestWintunHelper$", "-test.v")
	cmd.Env = append(os.Environ(), helperModeEnv+"="+mode)
	out, err := cmd.CombinedOutput()
	t.Logf("x64 %s:\n%s", mode, out)
	return err
}

// openconnectFinds: openconnect's search for an adapter by name (tun-win32.c), which opens one
// it finds instead of creating a new one.
func openconnectFinds(t *testing.T, name string) bool {
	class, err := registry.OpenKey(registry.LOCAL_MACHINE,
		`SYSTEM\CurrentControlSet\Control\Class\{4D36E972-E325-11CE-BFC1-08002BE10318}`, registry.ENUMERATE_SUB_KEYS)
	if err != nil {
		t.Fatal(err)
	}
	defer class.Close()
	keys, err := class.ReadSubKeyNames(-1)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range keys {
		driver, err := registry.OpenKey(class, key, registry.QUERY_VALUE)
		if err != nil {
			continue
		}
		component, _, _ := driver.GetStringValue("ComponentId")
		guid, _, _ := driver.GetStringValue("NetCfgInstanceId")
		driver.Close()
		if !strings.EqualFold(component, "wintun") || guid == "" {
			continue
		}
		connection, err := registry.OpenKey(registry.LOCAL_MACHINE, networkKey+guid+`\Connection`, registry.QUERY_VALUE)
		if err != nil {
			continue
		}
		found, _, _ := connection.GetStringValue("Name")
		connection.Close()
		if found == name {
			return true
		}
	}
	return false
}

func TestLeftoverAdapterOfEmulatedWintunIsRemoved(t *testing.T) {
	if os.Getenv(wintunDLLEnv) == "" || os.Getenv(x64TestEnv) == "" {
		t.Skip("needs administrator rights on Windows on ARM; set by CI")
	}
	t.Cleanup(func() { _, _ = removeAdapter(testAdapter) })
	if err := runHelper(t, "create-close"); err != nil {
		t.Fatal(err)
	}
	// What a user on Windows on ARM had after the first connection.
	if !openconnectFinds(t, testAdapter) {
		t.Fatal("Wintun removed its adapter itself: the leftover did not occur")
	}
	if err := runHelper(t, "open"); err != nil {
		t.Fatal("the leftover opens:", err)
	}
	n, err := removeAdapter(testAdapter)
	if err != nil || n != 1 {
		t.Fatal(n, err)
	}
	if openconnectFinds(t, testAdapter) {
		t.Fatal("still found after the removal")
	}
	if err := runHelper(t, "create-open"); err != nil {
		t.Fatal("no new adapter after the removal:", err)
	}
	if n, err := removeAdapter(testAdapter); err != nil || n != 1 {
		t.Fatal("second leftover", n, err)
	}
}
