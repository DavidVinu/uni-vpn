//go:build windows

package wintunnel

import (
	"errors"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// guidDevclassNet is GUID_DEVCLASS_NET, the class of network adapters.
var guidDevclassNet = windows.GUID{Data1: 0x4d36e972, Data2: 0xe325, Data3: 0x11ce,
	Data4: [8]byte{0xbf, 0xc1, 0x08, 0x00, 0x2b, 0xe1, 0x03, 0x18}}

const (
	wintunPrefix = `SWD\WINTUN\`
	// networkKey holds each adapter's connection name, which openconnect looks up.
	networkKey = `SYSTEM\CurrentControlSet\Control\Network\{4D36E972-E325-11CE-BFC1-08002BE10318}\`
)

// removeAdapter removes Wintun adapters named name that are no longer present. Wintun removes
// its adapter when openconnect exits, but under x64 emulation (Windows on ARM) it does so
// through a native helper that fails to start. What stays keeps the name, so the next
// openconnect finds it, cannot open it and gives up ("Could not open Wintun adapter"). This
// core runs natively there, where removing it works. Returns how many it removed.
func removeAdapter(name string) (int, error) {
	present, err := wintunDevices(windows.DIGCF_PRESENT)
	if err != nil {
		return 0, err
	}
	set, err := windows.SetupDiGetClassDevsEx(&guidDevclassNet, "", 0, 0, 0, "")
	if err != nil {
		return 0, err
	}
	defer set.Close()
	removed := 0
	var errs []error
	for i := 0; ; i++ {
		data, err := set.EnumDeviceInfo(i)
		if errors.Is(err, windows.ERROR_NO_MORE_ITEMS) {
			break
		}
		if err != nil {
			continue
		}
		id, err := set.DeviceInstanceID(data)
		if err != nil || !isWintun(id) || present[strings.ToUpper(id)] || !strings.EqualFold(adapterName(set, data), name) {
			continue
		}
		params := windows.RemoveDeviceParams{ClassInstallHeader: *windows.MakeClassInstallHeader(windows.DIF_REMOVE),
			Scope: windows.DI_REMOVEDEVICE_GLOBAL}
		if err := set.SetClassInstallParams(data, &params.ClassInstallHeader, uint32(unsafe.Sizeof(params))); err != nil {
			errs = append(errs, err)
			continue
		}
		if err := set.CallClassInstaller(windows.DIF_REMOVE, data); err != nil {
			errs = append(errs, err)
			continue
		}
		removed++
	}
	return removed, errors.Join(errs...)
}

func isWintun(id string) bool {
	return len(id) > len(wintunPrefix) && strings.EqualFold(id[:len(wintunPrefix)], wintunPrefix)
}

// wintunDevices returns the instance IDs (upper case) of the Wintun adapters set up with flags.
func wintunDevices(flags windows.DIGCF) (map[string]bool, error) {
	set, err := windows.SetupDiGetClassDevsEx(&guidDevclassNet, "", 0, flags, 0, "")
	if err != nil {
		return nil, err
	}
	defer set.Close()
	ids := map[string]bool{}
	for i := 0; ; i++ {
		data, err := set.EnumDeviceInfo(i)
		if errors.Is(err, windows.ERROR_NO_MORE_ITEMS) {
			return ids, nil
		}
		if err != nil {
			continue
		}
		if id, err := set.DeviceInstanceID(data); err == nil && isWintun(id) {
			ids[strings.ToUpper(id)] = true
		}
	}
}

// adapterName is the adapter's connection name, as openconnect reads it: NetCfgInstanceId from
// the driver key, then the name under the network key. "" when either is missing.
func adapterName(set windows.DevInfo, data *windows.DevInfoData) string {
	handle, err := set.OpenDevRegKey(data, windows.DICS_FLAG_GLOBAL, 0, windows.DIREG_DRV, windows.KEY_QUERY_VALUE)
	if err != nil {
		return ""
	}
	driver := registry.Key(handle)
	defer driver.Close()
	guid, _, err := driver.GetStringValue("NetCfgInstanceId")
	if err != nil {
		return ""
	}
	connection, err := registry.OpenKey(registry.LOCAL_MACHINE, networkKey+guid+`\Connection`, registry.QUERY_VALUE)
	if err != nil {
		return ""
	}
	defer connection.Close()
	name, _, _ := connection.GetStringValue("Name")
	return name
}
