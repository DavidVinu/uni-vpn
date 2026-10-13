//go:build windows

package sockowner

import (
	"errors"
	"net"
	"os"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var procGetExtendedTcpTable = windows.NewLazySystemDLL("iphlpapi.dll").NewProc("GetExtendedTcpTable")

const tcpTableOwnerPIDConnections = 4 // TCP_TABLE_OWNER_PID_CONNECTIONS

// self is this process; the table names processes.
var self = uint32(os.Getpid())

// OtherAccount reports whether the client's end of c, a connection accepted on 127.0.0.1,
// belongs to another account. An error means the system could not tell.
//
// uni-vpn runs as the user, who may not open another account's processes. Their sign-in
// session tells the account instead, and anyone may read it: each signed-in account has its
// own, and the scheduled task runs uni-vpn in the user's (InteractiveToken).
func OtherAccount(c net.Conn) (bool, error) {
	r, err := clientEnd(c)
	if err != nil {
		return false, err
	}
	if !r.known {
		return otherOwner(r, self), nil
	}
	sessions, err := sessions()
	if err != nil {
		return false, err
	}
	ours, ok := sessions[self]
	if !ok || ours == 0 {
		return false, errNoSession
	}
	theirs, ok := sessions[r.owner]
	if !ok {
		return false, errors.New("the client's program has ended")
	}
	return theirs != ours, nil
}

// clientEnd finds the client's end of c in the TCP tables; a dual-stack client (an IPv6
// socket to ::ffff:127.0.0.1) may be in the IPv6 one.
func clientEnd(c net.Conn) (row, error) {
	client, server, err := ends(c)
	if err != nil {
		return row{}, err
	}
	rows, err := tcpTable(windows.AF_INET, parseTCPTable)
	if err != nil {
		return row{}, err
	}
	if more, err := tcpTable(windows.AF_INET6, parseTCP6Table); err == nil {
		rows = append(rows, more...)
	}
	return find(rows, client, server, self)
}

func tcpTable(family uint32, parse func([]byte) ([]row, error)) ([]row, error) {
	if err := procGetExtendedTcpTable.Find(); err != nil {
		return nil, err
	}
	size := uint32(16 << 10)
	for range 5 {
		buf := make([]byte, size)
		r, _, _ := procGetExtendedTcpTable.Call(uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size)), 0,
			uintptr(family), tcpTableOwnerPIDConnections, 0)
		if syscall.Errno(r) == windows.ERROR_INSUFFICIENT_BUFFER {
			size += 4 << 10 // connections may open meanwhile
			continue
		}
		if r != 0 {
			return nil, syscall.Errno(r)
		}
		return parse(buf)
	}
	return nil, errors.New("TCP table keeps growing")
}

// sessions maps process ids to their sign-in session.
func sessions() (map[uint32]uint32, error) {
	size := uint32(512 << 10)
	for range 5 {
		buf := make([]byte, size)
		err := windows.NtQuerySystemInformation(windows.SystemProcessInformation, unsafe.Pointer(&buf[0]), size, &size)
		if err == windows.STATUS_INFO_LENGTH_MISMATCH {
			size += 64 << 10 // processes may start meanwhile
			continue
		}
		if err != nil {
			return nil, err
		}
		found := map[uint32]uint32{}
		for off := uintptr(0); off+unsafe.Sizeof(windows.SYSTEM_PROCESS_INFORMATION{}) <= uintptr(len(buf)); {
			p := (*windows.SYSTEM_PROCESS_INFORMATION)(unsafe.Pointer(&buf[off]))
			found[uint32(p.UniqueProcessID)] = p.SessionID
			if p.NextEntryOffset == 0 {
				break
			}
			off += uintptr(p.NextEntryOffset)
		}
		return found, nil
	}
	return nil, errors.New("process list keeps growing")
}
