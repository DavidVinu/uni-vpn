// Package credentials keeps the password and the TOTP secret in the OS keyring: GNOME
// keyring/KDE (secret-tool), macOS keychain (security) or Windows Credential Manager.
// It mirrors uni_vpn/credentials.py; service names, attributes, labels and target names
// must stay identical so both cores read and write the same entries.
//
// Two separate entries, each with its own service name: secret-tool searches by attribute
// sets, so an entry with an extra attribute would also match the search for the password.
package credentials

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"runtime"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/DavidVinu/uni-vpn/internal/platform"
)

// Kind selects one of the two keyring entries.
type Kind string

const (
	KindPassword Kind = "password"
	KindTOTP     Kind = "totp"
)

const (
	Service = "uni-vpn"
	Label   = "Uni VPN"
)

var (
	Services = map[Kind]string{KindPassword: Service, KindTOTP: "uni-vpn-totp"}
	Labels   = map[Kind]string{KindPassword: Label, KindTOTP: "Uni VPN (second factor)"}
)

// CommandTimeout bounds store and delete: a keyring dialog nobody sees (service without
// GUI, CI) must not block anything forever.
const CommandTimeout = 60 * time.Second

// ErrSecretMissing matches every MissingError (Python: SecretMissing).
var ErrSecretMissing = errors.New("secret missing")

// MissingError means no entry of that kind is saved (Python: PasswordMissing, TotpMissing).
type MissingError struct{ Kind Kind }

var (
	ErrPasswordMissing = &MissingError{Kind: KindPassword}
	ErrTotpMissing     = &MissingError{Kind: KindTOTP}
)

func (e *MissingError) Error() string {
	if e.Kind == KindTOTP {
		return "No TOTP secret saved"
	}
	return "No password saved"
}

// Is lets errors.Is match ErrSecretMissing and the sentinel of the same kind.
func (e *MissingError) Is(target error) bool {
	if target == ErrSecretMissing {
		return true
	}
	if m, ok := target.(*MissingError); ok {
		return m.Kind == e.Kind
	}
	return false
}

// LockedError means the keyring did not answer in time (Python: KeyringLocked).
type LockedError struct{ Msg string }

func (e *LockedError) Error() string { return e.Msg }

// KeyringError is any other keyring failure (Python: KeyringError).
type KeyringError struct{ Msg string }

func (e *KeyringError) Error() string { return e.Msg }

// CredentialError is a failed Credential Manager call (Python: windows.CredentialError).
type CredentialError struct{ Msg string }

func (e *CredentialError) Error() string { return e.Msg }

// ErrTimeout is what a Runner returns when the command ran into its timeout.
var ErrTimeout = errors.New("command timed out")

// Result of a finished command. Code is negative for a signal, like Python's returncode.
type Result struct {
	Code   int
	Stdout []byte
	Stderr []byte
}

// Runner runs cmd with input on stdin (nil: no input) and kills it after timeout,
// returning ErrTimeout then. A non-zero exit status is not an error.
type Runner func(cmd []string, input []byte, timeout time.Duration) (Result, error)

// CredStore is the Windows Credential Manager. Read returns nil, nil when there is no entry.
type CredStore interface {
	Read(target string) ([]byte, error)
	Write(target, user string, secret []byte, comment string) error
	Delete(target string) bool
}

// Keyring holds the injectable parts; the zero value uses the real system.
type Keyring struct {
	Run        Runner                   // nil: ExecRunner
	FindBinary func(name string) string // nil: platform.FindBinary(name, "")
	GOOS       string                   // "": runtime.GOOS
	Cred       CredStore                // nil: the native Credential Manager
}

// Default is the keyring the package level functions use.
var Default = &Keyring{}

func (k *Keyring) goos() string {
	if k.GOOS != "" {
		return k.GOOS
	}
	return runtime.GOOS
}

func (k *Keyring) isMacOS() bool   { return k.goos() == "darwin" }
func (k *Keyring) isWindows() bool { return k.goos() == "windows" }

func (k *Keyring) run(cmd []string, input []byte, timeout time.Duration) (Result, error) {
	if k.Run != nil {
		return k.Run(cmd, input, timeout)
	}
	return ExecRunner(cmd, input, timeout)
}

func (k *Keyring) cred() CredStore {
	if k.Cred != nil {
		return k.Cred
	}
	return nativeCred{}
}

func (k *Keyring) secretTool() (string, error) {
	var tool string
	if k.FindBinary != nil {
		tool = k.FindBinary("secret-tool")
	} else {
		tool = platform.FindBinary("secret-tool", "")
	}
	if tool == "" {
		return "", &KeyringError{"secret-tool is missing (package libsecret-tools)"}
	}
	return tool, nil
}

func names(kind Kind) (service, label string, err error) {
	service, ok := Services[kind]
	if !ok {
		return "", "", fmt.Errorf("unknown secret kind %q", kind)
	}
	return service, Labels[kind], nil
}

// CredentialTarget is the Credential Manager target name of an entry.
func CredentialTarget(service, user string) string { return service + ":" + user }

// LookupCommand is the command that prints the secret.
func (k *Keyring) LookupCommand(user string, kind Kind) ([]string, error) {
	service, _, err := names(kind)
	if err != nil {
		return nil, err
	}
	if k.isMacOS() {
		return []string{platform.Security, "find-generic-password", "-s", service, "-a", user, "-w"}, nil
	}
	tool, err := k.secretTool()
	if err != nil {
		return nil, err
	}
	return []string{tool, "lookup", "service", service, "user", user}, nil
}

func (k *Keyring) getSecretWindows(ctx context.Context, user string, kind Kind, timeout time.Duration) ([]byte, error) {
	service, _, err := names(kind)
	if err != nil {
		return nil, err
	}
	type answer struct {
		out []byte
		err error
	}
	// Buffered: the call keeps running after a timeout, like Python's executor thread.
	done := make(chan answer, 1)
	target := CredentialTarget(service, user)
	store := k.cred()
	go func() {
		out, err := store.Read(target)
		done <- answer{out, err}
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case a := <-done:
		if a.err != nil {
			var ce *CredentialError
			if errors.As(a.err, &ce) {
				return nil, &KeyringError{ce.Msg}
			}
			return nil, a.err
		}
		if len(a.out) == 0 {
			return nil, &MissingError{Kind: kind}
		}
		return a.out, nil
	case <-timer.C:
		return nil, &LockedError{"Credential Manager did not answer"}
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// GetSecret reads one entry. command replaces the lookup command (tests); on Windows an
// empty command means Credential Manager.
func (k *Keyring) GetSecret(ctx context.Context, user string, kind Kind, timeout time.Duration, command []string) ([]byte, error) {
	if k.isWindows() && len(command) == 0 {
		return k.getSecretWindows(ctx, user, kind, timeout)
	}
	if _, _, err := names(kind); err != nil {
		return nil, err
	}
	cmd := command
	if len(cmd) == 0 {
		var err error
		if cmd, err = k.LookupCommand(user, kind); err != nil {
			return nil, err
		}
	}
	type answer struct {
		res Result
		err error
	}
	done := make(chan answer, 1)
	go func() {
		res, err := k.run(cmd, nil, timeout)
		done <- answer{res, err}
	}()
	var a answer
	select {
	case a = <-done:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if errors.Is(a.err, ErrTimeout) {
		return nil, &LockedError{"Keyring locked or access denied"}
	}
	if a.err != nil {
		return nil, a.err
	}
	out := a.res.Stdout
	if a.res.Code != 0 || len(out) == 0 {
		return nil, &MissingError{Kind: kind}
	}
	if k.isMacOS() && out[len(out)-1] == '\n' {
		out = out[:len(out)-1]
	}
	if k.isMacOS() && len(command) == 0 {
		out = KeychainText(out)
	}
	return out, nil
}

// KeychainText undoes the hex output of `security -w`: it prints a secret as hex when it
// holds anything but printable ASCII, for example "ä". A password that merely looks like
// hex decodes to printable ASCII, which security would have printed as it is, so that
// case keeps the original.
func KeychainText(out []byte) []byte {
	// Python's `$` also matches before one final newline; bytes.fromhex skips it.
	digits := bytes.TrimSuffix(out, []byte("\n"))
	if len(digits) == 0 || len(digits)%2 != 0 {
		return out
	}
	for _, c := range digits {
		if !('0' <= c && c <= '9' || 'a' <= c && c <= 'f') {
			return out
		}
	}
	decoded := make([]byte, len(digits)/2)
	if _, err := hex.Decode(decoded, digits); err != nil {
		return out
	}
	for _, b := range decoded {
		if b < 0x20 || b >= 0x7f {
			return decoded
		}
	}
	return out
}

func (k *Keyring) GetPassword(ctx context.Context, user string, timeout time.Duration, command []string) ([]byte, error) {
	return k.GetSecret(ctx, user, KindPassword, timeout, command)
}

func (k *Keyring) GetTotp(ctx context.Context, user string, timeout time.Duration, command []string) ([]byte, error) {
	return k.GetSecret(ctx, user, KindTOTP, timeout, command)
}

func quoteSecurity(value string) string {
	return strings.ReplaceAll(strings.ReplaceAll(value, `\`, `\\`), `"`, `\"`)
}

// StoreSecret saves one entry, replacing an existing one.
func (k *Keyring) StoreSecret(user string, kind Kind, value string) error {
	// Last line of defence: `security -i` reads commands line by line, openconnect
	// --passwd-on-stdin exactly one line. The CLI and HTTP API reject this earlier.
	if strings.ContainsAny(value, "\n\r") {
		return &KeyringError{"Password must not contain a line break"}
	}
	if strings.ContainsAny(user, "\n\r") {
		return &KeyringError{"University ID must not contain a line break"}
	}
	service, label, err := names(kind)
	if err != nil {
		return err
	}
	if k.isWindows() {
		// UTF-8 blob, not UTF-16: what the Python core writes.
		if err := k.cred().Write(CredentialTarget(service, user), user, []byte(value), label); err != nil {
			var ce *CredentialError
			if errors.As(err, &ce) {
				return &KeyringError{fmt.Sprintf("%s could not be saved: %s", label, ce.Msg)}
			}
			return err
		}
		return nil
	}
	var res Result
	if k.isMacOS() {
		// Delete first, then create anew: "-U" (update) triggers a confirmation dialog
		// on macOS that waits forever without a GUI (measured in CI).
		if _, err := k.DeleteSecret(user, kind); err != nil {
			return err
		}
		script := fmt.Sprintf("add-generic-password -a \"%s\" -s \"%s\" -T %s -w \"%s\"\n",
			quoteSecurity(user), service, platform.Security, quoteSecurity(value))
		res, err = k.run([]string{platform.Security, "-i"}, []byte(script), CommandTimeout)
	} else {
		tool, terr := k.secretTool()
		if terr != nil {
			return terr
		}
		cmd := []string{tool, "store", "--label", label, "service", service, "user", user}
		res, err = k.run(cmd, append([]byte{}, value...), CommandTimeout)
	}
	if errors.Is(err, ErrTimeout) {
		return &KeyringError{"No answer from the keyring (locked, or a dialog is waiting)"}
	}
	if err != nil {
		return err
	}
	if res.Code != 0 {
		detail := strings.TrimSpace(decodeReplace(res.Stderr))
		if detail == "" {
			detail = strconv.Itoa(res.Code)
		}
		return &KeyringError{fmt.Sprintf("%s could not be saved: %s", label, detail)}
	}
	return nil
}

func (k *Keyring) StorePassword(user, password string) error {
	return k.StoreSecret(user, KindPassword, password)
}

func (k *Keyring) StoreTotp(user, token string) error {
	return k.StoreSecret(user, KindTOTP, token)
}

// DeleteSecret removes one entry and reports whether it did. The error is only set when
// the command could not run at all (Python raised there too).
func (k *Keyring) DeleteSecret(user string, kind Kind) (bool, error) {
	service, _, err := names(kind)
	if err != nil {
		return false, err
	}
	if k.isWindows() {
		return k.cred().Delete(CredentialTarget(service, user)), nil
	}
	var cmd []string
	if k.isMacOS() {
		cmd = []string{platform.Security, "delete-generic-password", "-s", service, "-a", user}
	} else {
		tool, err := k.secretTool()
		if err != nil {
			return false, err
		}
		cmd = []string{tool, "clear", "service", service, "user", user}
	}
	res, err := k.run(cmd, nil, CommandTimeout)
	if errors.Is(err, ErrTimeout) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return res.Code == 0, nil
}

func (k *Keyring) DeletePassword(user string) (bool, error) {
	return k.DeleteSecret(user, KindPassword)
}
func (k *Keyring) DeleteTotp(user string) (bool, error) { return k.DeleteSecret(user, KindTOTP) }

// decodeReplace decodes UTF-8 with one U+FFFD per invalid byte, like bytes.decode(errors="replace").
func decodeReplace(b []byte) string {
	if utf8.Valid(b) {
		return string(b)
	}
	var sb strings.Builder
	for len(b) > 0 {
		r, size := utf8.DecodeRune(b)
		sb.WriteRune(r)
		b = b[size:]
	}
	return sb.String()
}

// Package level shortcuts on Default.

func LookupCommand(user string, kind Kind) ([]string, error) {
	return Default.LookupCommand(user, kind)
}

func GetSecret(ctx context.Context, user string, kind Kind, timeout time.Duration, command []string) ([]byte, error) {
	return Default.GetSecret(ctx, user, kind, timeout, command)
}

func GetPassword(ctx context.Context, user string, timeout time.Duration) ([]byte, error) {
	return Default.GetPassword(ctx, user, timeout, nil)
}

func GetTotp(ctx context.Context, user string, timeout time.Duration) ([]byte, error) {
	return Default.GetTotp(ctx, user, timeout, nil)
}

func StoreSecret(user string, kind Kind, value string) error {
	return Default.StoreSecret(user, kind, value)
}
func StorePassword(user, password string) error         { return Default.StorePassword(user, password) }
func StoreTotp(user, token string) error                { return Default.StoreTotp(user, token) }
func DeleteSecret(user string, kind Kind) (bool, error) { return Default.DeleteSecret(user, kind) }
func DeletePassword(user string) (bool, error)          { return Default.DeletePassword(user) }
func DeleteTotp(user string) (bool, error)              { return Default.DeleteTotp(user) }
