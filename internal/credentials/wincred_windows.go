//go:build windows

package credentials

import (
	"errors"
	"strings"
	"syscall"
	"unicode"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Mirrors the Credential Manager functions in uni_vpn/windows.py.

const (
	credTypeGeneric         = 1
	credPersistLocalMachine = 2
	errorNotFound           = 1168
)

var (
	advapi32       = windows.NewLazySystemDLL("advapi32.dll")
	procCredReadW  = advapi32.NewProc("CredReadW")
	procCredWriteW = advapi32.NewProc("CredWriteW")
	procCredDelete = advapi32.NewProc("CredDeleteW")
	procCredFree   = advapi32.NewProc("CredFree")
)

// credential is CREDENTIALW.
type credential struct {
	Flags              uint32
	Type               uint32
	TargetName         *uint16
	Comment            *uint16
	LastWritten        windows.Filetime
	CredentialBlobSize uint32
	CredentialBlob     *byte
	Persist            uint32
	AttributeCount     uint32
	Attributes         uintptr
	TargetAlias        *uint16
	UserName           *uint16
}

type nativeCred struct{}

// formatError matches ctypes.FormatError: system message in the user's language,
// trailing whitespace removed.
func formatError(code syscall.Errno) string {
	const langNeutralSublangDefault = 0x0400
	buf := make([]uint16, 4096)
	n, err := windows.FormatMessage(windows.FORMAT_MESSAGE_FROM_SYSTEM|windows.FORMAT_MESSAGE_IGNORE_INSERTS,
		0, uint32(code), langNeutralSublangDefault, buf, nil)
	if err != nil || n == 0 {
		return "<no description>"
	}
	return strings.TrimRightFunc(windows.UTF16ToString(buf[:n]), unicode.IsSpace)
}

func lastErrno(err error) syscall.Errno {
	var errno syscall.Errno
	if errors.As(err, &errno) {
		return errno
	}
	return 0
}

// Read returns the secret of a generic credential, nil if there is none.
func (nativeCred) Read(target string) ([]byte, error) {
	name, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return nil, err
	}
	var cred *credential
	r, _, callErr := procCredReadW.Call(uintptr(unsafe.Pointer(name)), credTypeGeneric, 0,
		uintptr(unsafe.Pointer(&cred)))
	if r == 0 {
		code := lastErrno(callErr)
		if code == errorNotFound {
			return nil, nil
		}
		return nil, &CredentialError{"CredRead failed: " + formatError(code)}
	}
	defer procCredFree.Call(uintptr(unsafe.Pointer(cred)))
	if cred.CredentialBlobSize == 0 || cred.CredentialBlob == nil {
		return []byte{}, nil
	}
	return append([]byte{}, unsafe.Slice(cred.CredentialBlob, cred.CredentialBlobSize)...), nil
}

func (nativeCred) Write(target, user string, secret []byte, comment string) error {
	name, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	userName, err := windows.UTF16PtrFromString(user)
	if err != nil {
		return err
	}
	cred := credential{
		Type:               credTypeGeneric,
		TargetName:         name,
		CredentialBlobSize: uint32(len(secret)),
		Persist:            credPersistLocalMachine,
		UserName:           userName,
	}
	if comment != "" {
		if cred.Comment, err = windows.UTF16PtrFromString(comment); err != nil {
			return err
		}
	}
	if len(secret) > 0 {
		cred.CredentialBlob = &secret[0]
	}
	r, _, callErr := procCredWriteW.Call(uintptr(unsafe.Pointer(&cred)), 0)
	if r == 0 {
		return &CredentialError{"CredWrite failed: " + formatError(lastErrno(callErr))}
	}
	return nil
}

func (nativeCred) Delete(target string) bool {
	name, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return false
	}
	r, _, _ := procCredDelete.Call(uintptr(unsafe.Pointer(name)), credTypeGeneric, 0)
	return r != 0
}
