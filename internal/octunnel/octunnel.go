//go:build libopenconnect

package octunnel

/*
#cgo linux pkg-config: openconnect
#cgo ios LDFLAGS: -framework Security -framework CoreFoundation
#cgo ios CFLAGS: -DUNI_VPN_SET_LOGIN
#include <errno.h>
#include <stdlib.h>
#include <string.h>
#include "shim.h"
*/
import "C"

import (
	"net/netip"
	"runtime"
	"runtime/cgo"
	"strings"
	"sync"
	"unsafe"
)

var initSSL = sync.OnceValue(func() C.int { return C.openconnect_init_ssl() })

// Session is one login and, after Connect, one CSTP connection.
type Session struct {
	u       *C.struct_uni_vpn
	handle  cgo.Handle
	lines   *lineWriter
	timeout int

	mu      sync.Mutex
	cmdFD   C.int // write end of the library's command pipe, -1 when gone
	freed   bool
	addr    netip.Addr
	changed bool // the gateway gave another address after reconnecting
	quit    string
}

// Trust stand-ins for the tests, which play the phone's trust store on Linux.
const (
	trustAccept = 1
	trustReject = 2
)

var testTrust int

// lastTrustHost is the name the last stand-in check was asked about.
func lastTrustHost() string { return C.GoString(C.uni_vpn_last_trust_host()) }

func newSession(o Options) *Session {
	s := &Session{timeout: o.ReconnectTimeout, cmdFD: -1}
	s.lines = &lineWriter{fn: func(line string) {
		// The library names why it ends only in its goodbye to the gateway; main.c reads
		// vpninfo->quit_reason, which the library does not export.
		if reason, ok := strings.CutPrefix(line, "Send BYE packet: "); ok {
			s.mu.Lock()
			s.quit = reason
			s.mu.Unlock()
		}
		if o.Line != nil {
			o.Line(line)
		}
	}}
	if s.timeout <= 0 {
		s.timeout = 60
	}
	return s
}

//export goOutput
func goOutput(handle C.uintptr_t, text *C.char) {
	if s, ok := cgo.Handle(handle).Value().(*Session); ok {
		s.lines.write(C.GoString(text))
	}
}

//export goReconnected
func goReconnected(handle C.uintptr_t) {
	s, ok := cgo.Handle(handle).Value().(*Session)
	if !ok {
		return
	}
	// The netstack keeps the address it started with, so a new one cannot carry traffic: end
	// the session like a dropped tunnel and let the daemon connect again.
	addr, _, _ := s.IPInfo()
	s.mu.Lock()
	changed := addr != s.addr
	s.changed = s.changed || changed
	s.mu.Unlock()
	if changed {
		s.line("VPN address changed from " + s.addr.String() + " to " + addr.String() + " on reconnect; ending the session")
		s.Cancel()
	}
}

func (s *Session) line(text string) { s.lines.write(text + "\n") }

// cBytes is a C copy of b with a terminating zero, for wipe.
func cBytes(b []byte) *C.char {
	p := (*C.char)(C.malloc(C.size_t(len(b) + 1)))
	buf := unsafe.Slice((*byte)(unsafe.Pointer(p)), len(b)+1)
	copy(buf, b)
	buf[len(b)] = 0
	return p
}

func wipe(p *C.char, n int) {
	C.memset(unsafe.Pointer(p), 0, C.size_t(n))
	C.free(unsafe.Pointer(p))
}

func cString(s string) *C.char {
	if s == "" {
		return nil
	}
	return C.CString(s)
}

// exitCode maps a negative errno from the library to the program's exit code and line, like
// the end of main.c.
func (s *Session) exitCode(ret C.int) int {
	switch -ret {
	case C.EPERM:
		s.line("Cookie was rejected by server; exiting.")
		return 2
	case C.EPIPE:
		s.line("Session terminated by server; exiting.")
		return 1
	case C.EINTR:
		s.line("User cancelled (SIGINT/SIGTERM); exiting.")
		return 0
	case C.ECONNABORTED:
		s.line("User detached from session (SIGHUP); exiting.")
		return 0
	case C.EIO:
		s.line("Unrecoverable I/O error; exiting.")
		return 1
	}
	s.mu.Lock()
	quit := s.quit
	s.mu.Unlock()
	if quit != "" {
		s.line(quit + "; exiting")
	} else {
		s.line("Unknown error; exiting.")
	}
	return 1
}

// Connect logs in (obtains the cookie) and makes the CSTP connection. On failure the lines
// tell why and the error is an *ExitError with the program's exit code.
func Connect(o Options) (*Session, error) {
	s := newSession(o)
	fail := func(code int, reason string) (*Session, error) {
		s.Free()
		return nil, &ExitError{Code: code, Reason: reason}
	}
	if initSSL() != 0 {
		s.line("Failed to initialise SSL library")
		return fail(1, "ssl")
	}
	s.handle = cgo.NewHandle(s)

	useragent, user, group := cString(o.UserAgent), cString(o.User), cString(o.AuthGroup)
	second, password := cBytes([]byte(o.SecondPassword)), cBytes(o.Password)
	noExternalAuth := C.int(0)
	if o.NoExternalAuth {
		noExternalAuth = 1
	}
	s.u = C.uni_vpn_new(C.uintptr_t(s.handle), useragent, user, group, password, second, noExternalAuth)
	wipe(password, len(o.Password))
	wipe(second, len(o.SecondPassword))
	if s.u != nil && testTrust != 0 {
		C.uni_vpn_test_trust(s.u, C.int(testTrust))
	}
	for _, p := range []*C.char{user, group} {
		C.free(unsafe.Pointer(p))
	}
	if s.u == nil {
		C.free(unsafe.Pointer(useragent))
		return fail(1, "out of memory")
	}
	vpn := s.u.vpninfo
	// Exactly the profile's user agent, as --useragent sets it; the one given to
	// openconnect_vpninfo_new gets the library's version appended.
	if useragent != nil {
		C.openconnect_set_useragent(vpn, useragent)
	}
	C.free(unsafe.Pointer(useragent))

	C.openconnect_set_loglevel(vpn, C.PRG_INFO)
	proto := C.CString("anyconnect")
	C.openconnect_set_protocol(vpn, proto)
	C.free(unsafe.Pointer(proto))
	if o.OS != "" {
		os := C.CString(o.OS)
		ret := C.openconnect_set_reported_os(vpn, os)
		C.free(unsafe.Pointer(os))
		if ret != 0 {
			s.line("Invalid OS identity \"" + o.OS + "\"")
			s.line("Allowed values: linux, linux-64, win, mac-intel, android, apple-ios")
			return fail(1, "os")
		}
	}
	if o.TOTPSecret != "" {
		secret := cBytes([]byte(o.TOTPSecret))
		ret := C.openconnect_set_token_mode(vpn, C.OC_TOKEN_MODE_TOTP, secret)
		wipe(secret, len(o.TOTPSecret))
		switch {
		case ret == -C.EINVAL:
			s.line("Soft token string is invalid")
			return fail(1, "token")
		case ret != 0:
			s.line("General failure in TOTP/HOTP support")
			return fail(1, "token")
		}
	}
	C.openconnect_disable_dtls(vpn)
	C.openconnect_set_dpd(vpn, 30)

	fd := C.openconnect_setup_cmd_pipe(vpn)
	if fd < 0 {
		return fail(1, "command pipe")
	}
	s.mu.Lock()
	s.cmdFD = fd
	s.mu.Unlock()
	if o.Cancel != nil {
		done := make(chan struct{})
		defer close(done)
		go func() {
			select {
			case <-o.Cancel:
				s.Cancel()
			case <-done:
			}
		}()
	}

	server := C.CString(o.Host)
	ret := C.openconnect_parse_url(vpn, server)
	C.free(unsafe.Pointer(server))
	if ret != 0 {
		return fail(1, "url")
	}
	// --usergroup, unless the URL brought a path (main.c keeps that order).
	if o.UserGroup != "" && C.openconnect_get_urlpath(vpn) == nil {
		group := C.CString(o.UserGroup)
		C.openconnect_set_urlpath(vpn, group)
		C.free(unsafe.Pointer(group))
	}
	if C.openconnect_obtain_cookie(vpn) != 0 {
		s.line("Failed to complete authentication")
		return fail(1, "authentication")
	}
	if ret := C.openconnect_make_cstp_connection(vpn); ret != 0 {
		s.line("Creating SSL connection failed")
		return fail(s.exitCode(ret), "cstp")
	}
	addr, _, _ := s.IPInfo()
	s.mu.Lock()
	s.addr = addr
	s.mu.Unlock()
	return s, nil
}

// IPInfo is the address, DNS servers and MTU the gateway assigned.
func (s *Session) IPInfo() (addr netip.Addr, dns []netip.Addr, mtu int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.freed || s.u == nil {
		return
	}
	var a [64]C.char
	var d [3 * 64]C.char
	var m C.int
	if C.uni_vpn_ip_info(s.u, &a[0], C.int(len(a)), &d[0], C.int(len(d)), &m) != 0 {
		return
	}
	addr, _ = netip.ParseAddr(C.GoString(&a[0]))
	for _, f := range strings.Fields(C.GoString(&d[0])) {
		if ip, err := netip.ParseAddr(f); err == nil {
			dns = append(dns, ip)
		}
	}
	return addr, dns, int(m)
}

// Run passes packets between the CSTP connection and tunFD (a packet socket that stays the
// caller's to close) until cancelled or the connection is dead, reconnecting on the way like
// the program. It returns the program's exit code: 0 after Cancel.
func (s *Session) Run(tunFD int) int {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	vpn := s.u.vpninfo
	if C.openconnect_setup_tun_fd(vpn, C.int(tunFD)) != 0 {
		s.line("Set up tun device failed")
		return 1
	}
	for {
		ret := C.openconnect_mainloop(vpn, C.int(s.timeout), C.RECONNECT_INTERVAL_MIN)
		if ret != 0 {
			s.mu.Lock()
			changed := s.changed
			s.mu.Unlock()
			if changed {
				return 1
			}
			return s.exitCode(ret)
		}
		s.line("User requested reconnect")
	}
}

func (s *Session) command(cmd byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.freed && s.cmdFD >= 0 {
		C.uni_vpn_write_cmd(s.cmdFD, C.char(cmd))
	}
}

// Cancel ends the login or Run (OC_CMD_CANCEL, as on SIGTERM).
func (s *Session) Cancel() { s.command(C.OC_CMD_CANCEL) }

// Reconnect drops the connection and makes it again (OC_CMD_PAUSE, as on SIGUSR2).
func (s *Session) Reconnect() { s.command(C.OC_CMD_PAUSE) }

// Free releases the library's state. The session cannot be used afterwards.
func (s *Session) Free() {
	s.mu.Lock()
	if s.freed {
		s.mu.Unlock()
		return
	}
	s.freed = true
	u := s.u
	s.u, s.cmdFD = nil, -1
	s.mu.Unlock()
	if u != nil {
		C.uni_vpn_free(u) // may still print, so the handle goes after it
	}
	if s.handle != 0 {
		s.handle.Delete()
	}
	s.lines.flush()
}
