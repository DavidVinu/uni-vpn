//go:build windows

package libtunnel

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

// sockBuf: room for a burst of packets in each direction, as on the other systems.
const sockBuf = 256 << 10

// socketPair returns the network stack's end and openconnect's end of a message-mode named
// pipe, one message per packet: Windows has no datagram socketpair, and libopenconnect does
// overlapped ReadFile and WriteFile on its tun handle (packaging/windows/openconnect-pipe-tun.patch).
// Both ends are overlapped, so closing the stack's end interrupts its reads. Only this account
// may open the pipe, there is one instance, and it is connected before anyone else could.
func socketPair() (*os.File, int, error) {
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return nil, -1, err
	}
	name, err := windows.UTF16PtrFromString(`\\.\pipe\uni-vpn-tunnel-` + hex.EncodeToString(random))
	if err != nil {
		return nil, -1, err
	}
	sa, err := ownerOnly()
	if err != nil {
		return nil, -1, err
	}
	theirs, err := windows.CreateNamedPipe(name,
		windows.PIPE_ACCESS_DUPLEX|windows.FILE_FLAG_OVERLAPPED|windows.FILE_FLAG_FIRST_PIPE_INSTANCE,
		windows.PIPE_TYPE_MESSAGE|windows.PIPE_READMODE_MESSAGE|windows.PIPE_WAIT|windows.PIPE_REJECT_REMOTE_CLIENTS,
		1, sockBuf, sockBuf, 0, sa)
	if err != nil {
		return nil, -1, fmt.Errorf("pipe: %w", err)
	}
	mine, err := windows.CreateFile(name, windows.GENERIC_READ|windows.GENERIC_WRITE, 0, nil,
		windows.OPEN_EXISTING, windows.FILE_FLAG_OVERLAPPED, 0)
	if err != nil {
		windows.CloseHandle(theirs)
		return nil, -1, fmt.Errorf("pipe client: %w", err)
	}
	mode := uint32(windows.PIPE_READMODE_MESSAGE)
	if err := windows.SetNamedPipeHandleState(mine, &mode, nil, nil); err != nil {
		windows.CloseHandle(mine)
		windows.CloseHandle(theirs)
		return nil, -1, fmt.Errorf("pipe mode: %w", err)
	}
	return os.NewFile(uintptr(mine), "netstack"), int(theirs), nil
}

// ownerOnly is a security descriptor that lets only this account (and SYSTEM) open the pipe.
func ownerOnly() (*windows.SecurityAttributes, error) {
	token := windows.GetCurrentProcessToken()
	user, err := token.GetTokenUser()
	if err != nil {
		return nil, err
	}
	sd, err := windows.SecurityDescriptorFromString("D:P(A;;GA;;;SY)(A;;GA;;;" + user.User.Sid.String() + ")")
	if err != nil {
		return nil, err
	}
	sa := &windows.SecurityAttributes{SecurityDescriptor: sd}
	sa.Length = uint32(unsafe.Sizeof(*sa))
	return sa, nil
}

func closeFD(fd int) { _ = windows.CloseHandle(windows.Handle(fd)) }
