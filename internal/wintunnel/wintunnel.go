// Package wintunnel is the tunnel on Windows: openconnect with a Wintun adapter plus the
// built-in SOCKS server. It mirrors uni_vpn/wintunnel.py.
//
// openconnect cannot pass the tunnel to a program on Windows (no --script-tun), so it creates
// a Wintun adapter (administrator rights, hence the elevated scheduled task). Our
// bin/uni-vpn-vpnc.js only gives the adapter its address and a default route with a metric so
// high that no other program uses it, and writes address and DNS servers to a file. The SOCKS
// server then connects from that address, which Windows sends through the adapter.
package wintunnel

import (
	"context"
	"fmt"
	"log/slog"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/DavidVinu/uni-vpn/internal/platform"
	"github.com/DavidVinu/uni-vpn/internal/tunnel"
)

const (
	Interface   = "uni-vpn"
	StatePrefix = "tunnel-"
)

// SocksServer is the running SOCKS server (package socks). Update changes the source address
// and DNS servers it uses for new connections; Stop closes the listener and open connections.
type SocksServer interface {
	Update(source string, dns []string)
	Stop()
}

// SocksFactory starts a SOCKS server on 127.0.0.1:port that connects from source and resolves
// names with dns. An error means the port could not be bound.
type SocksFactory func(port int, source string, dns []string) (SocksServer, error)

// isLineBreak reports the characters Python's str.splitlines splits on.
func isLineBreak(r rune) bool {
	switch r {
	case '\n', '\r', '\v', '\f', 0x1c, 0x1d, 0x1e, 0x85, 0x2028, 0x2029:
		return true
	}
	return false
}

func splitLines(text string) []string {
	var lines []string
	for text != "" {
		i := strings.IndexFunc(text, isLineBreak)
		if i < 0 {
			lines = append(lines, text)
			break
		}
		lines = append(lines, text[:i])
		_, size := utf8.DecodeRuneInString(text[i:])
		if strings.HasPrefix(text[i:], "\r\n") {
			size = 2
		}
		text = text[i+size:]
	}
	return lines
}

// pyStrip strips like Python's str.strip() without arguments.
func pyStrip(s string) string {
	return strings.TrimFunc(s, func(r rune) bool {
		switch r {
		case ' ', '\t', '\n', '\r', '\v', '\f', 0x1c, 0x1d, 0x1e, 0x1f, 0x85, 0xa0, 0x1680, 0x2028, 0x2029, 0x202f, 0x205f, 0x3000:
			return true
		}
		return r >= 0x2000 && r <= 0x200a
	})
}

// ParseState reads the KEY=VALUE lines the vpnc script writes.
func ParseState(text string) map[string]string {
	values := map[string]string{}
	for _, line := range splitLines(text) {
		if key, value, ok := strings.Cut(line, "="); ok {
			values[pyStrip(key)] = pyStrip(value)
		}
	}
	return values
}

// DNSServers returns IPv4 servers only: openconnect also lists IPv6 ones here, the SOCKS
// source is IPv4.
func DNSServers(values map[string]string) []string {
	var servers []string
	for _, s := range strings.Fields(values["INTERNAL_IP4_DNS"]) {
		if a, err := netip.ParseAddr(s); err == nil && a.Is4() {
			servers = append(servers, a.String())
		}
	}
	return servers
}

// ChildEnvKeep: environment passed to the elevated openconnect (and its vpnc script): only
// these names, and the system ones with trusted values. User variables override system ones on
// Windows, so for example ComSpec, PATH or a library search path could otherwise run user code
// elevated.
var ChildEnvKeep = []string{"TEMP", "TMP", "SystemDrive", "ProgramData", "ProgramFiles", "ProgramFiles(x86)",
	"ProgramW6432", "NUMBER_OF_PROCESSORS", "PROCESSOR_ARCHITECTURE", "OS", "USERNAME",
	"COMPUTERNAME"}

// TrustedEnv is windows.trusted_env of the Python core. system and windows are System32 and
// the Windows folder from the API, not from variables the user can set.
func TrustedEnv(current map[string]string, system, windows string) map[string]string {
	lower := map[string]string{}
	for k, v := range current {
		lower[strings.ToLower(k)] = v
	}
	env := map[string]string{}
	for _, k := range ChildEnvKeep {
		if v, ok := lower[strings.ToLower(k)]; ok {
			env[k] = v
		}
	}
	env["SystemRoot"] = windows
	env["windir"] = windows
	env["ComSpec"] = filepath.Join(system, "cmd.exe")
	env["PATH"] = strings.Join([]string{system, windows, filepath.Join(system, "Wbem"),
		filepath.Join(system, "WindowsPowerShell", "v1.0")}, ";")
	env["PATHEXT"] = ".COM;.EXE;.BAT;.CMD;.VBS;.JS;.WSF"
	return env
}

func envMap(env []string) map[string]string {
	m := map[string]string{}
	for _, kv := range env {
		// Windows has entries like "=C:=C:\dir"; the key ends at the first "=" after the start.
		if i := strings.Index(kv[min(1, len(kv)):], "="); i >= 0 {
			i += min(1, len(kv))
			m[kv[:i]] = kv[i+1:]
		}
	}
	return m
}

func envList(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k, v := range m {
		out = append(out, k+"="+v)
	}
	slices.Sort(out)
	return out
}

// Tunnel is the Windows tunnel. Fields must be set before Start.
type Tunnel struct {
	*tunnel.Tunnel
	Script string
	// CtrlC sends Ctrl+C to openconnect's console and reports whether that worked.
	// New sets the default: on Windows a helper process (see CtrlCHelperArg).
	CtrlC func(pid int) bool
	// NewSocks starts the SOCKS server; required.
	NewSocks SocksFactory

	mu        sync.Mutex
	stateFile string
	socks     SocksServer
	source    string
	dns       []string
	cancel    context.CancelFunc
}

// New creates a Windows tunnel. tokenDir "" means platform.StateDir().
func New(cfg tunnel.Config, openconnect, script string, log *slog.Logger, tokenDir string) *Tunnel {
	w := &Tunnel{Tunnel: tunnel.New(cfg, openconnect, script, log, "", tokenDir), Script: script, CtrlC: defaultCtrlC}
	w.Hooks = tunnel.Hooks{
		Command:       w.command,
		Env:           w.env,
		PasswordBytes: encodePassword,
		Prepare:       prepare,
		AfterExit:     w.stopServer, // openconnect is gone (logout, server, crash): the source address no longer exists.
		KillWrapper:   func() {},
	}
	return w
}

func (w *Tunnel) logf(level slog.Level, format string, args ...any) {
	w.Log.Log(context.Background(), level, fmt.Sprintf(format, args...))
}

// Command is the openconnect argv; port is unused because the script reports the address.
func (w *Tunnel) Command(port int) []string { return w.command(port) }

func (w *Tunnel) command(int) []string {
	cmd := []string{w.OpenConnect}
	cmd = append(cmd, w.AuthArgs()...)
	cmd = append(cmd,
		"--no-dtls",
		"--force-dpd=30",
		"--reconnect-timeout=60",
		// The script configures IPv4 only; an IPv6 address would be left half set up.
		"--disable-ipv6",
		"--interface="+Interface,
		// openconnect 9.12 runs it as: cscript.exe "<script>" (the .js association picks JScript)
		"--script="+w.Script,
	)
	cmd = append(cmd, w.TokenArgs()...)
	return append(cmd, w.Cfg.Host)
}

func (w *Tunnel) env(env []string, port int) []string {
	if platform.IsWindows {
		system, windows := systemDirs()
		env = envList(TrustedEnv(envMap(env), system, windows))
	}
	stateFile := filepath.Join(w.TokenDir, StatePrefix+strconv.Itoa(port)+".env")
	w.mu.Lock()
	w.stateFile = stateFile
	w.mu.Unlock()
	_ = os.Remove(stateFile)
	return tunnel.SetEnv(env, "UNI_VPN_STATE", stateFile)
}

// StateFile is where the vpnc script reports the tunnel address, "" before Start.
func (w *Tunnel) StateFile() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.stateFile
}

// Start launches openconnect, then follows the script's state file in the background.
func (w *Tunnel) Start(password []byte, totp string) error {
	if err := os.MkdirAll(w.TokenDir, 0o777); err != nil {
		return err
	}
	if err := w.Tunnel.Start(password, totp); err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	w.mu.Lock()
	w.cancel = cancel
	w.mu.Unlock()
	go w.serveWhenUp(ctx)
	return nil
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}

// serveWhenUp starts the SOCKS server on the tunnel port as soon as the script reports the
// address, then follows it.
func (w *Tunnel) serveWhenUp(ctx context.Context) {
	reported := ""
	port := w.Port()
	for !w.HasExited() {
		if ctx.Err() != nil {
			return
		}
		values := w.readState()
		w.mu.Lock()
		up := w.socks != nil
		w.mu.Unlock()
		if e := values["ERROR"]; e != "" && up {
			// After a reconnect of openconnect: the tunnel may still work, so keep it.
			if e != reported {
				reported = e
				w.logf(slog.LevelWarn, "vpnc script after reconnect: %s", reported)
			}
		} else if e != "" {
			// The script could not set up the adapter; without it nothing would work.
			w.logf(slog.LevelError, "Tunnel setup failed: %s", e)
			w.SetClassification(tunnel.Verdict{State: tunnel.StateError, Message: "Tunnel setup failed: " + e})
			w.endProcess(w.Cfg.StopGrace)
			return
		}
		address, dns := values["INTERNAL_IP4_ADDRESS"], DNSServers(values)
		if address != "" && !up {
			server, err := w.NewSocks(port, address, dns)
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				w.logf(slog.LevelError, "SOCKS server on port %d failed: %v", port, err)
				w.SetClassification(tunnel.Verdict{State: tunnel.StateError,
					Message: fmt.Sprintf("Local proxy port %d not available: %v", port, err)})
				w.endProcess(w.Cfg.StopGrace)
				return
			}
			w.mu.Lock()
			if ctx.Err() != nil { // stopped while the server was starting
				w.mu.Unlock()
				server.Stop()
				return
			}
			w.socks, w.source, w.dns = server, address, dns // only now: WaitReady takes it as the ready signal
			w.mu.Unlock()
			w.logf(slog.LevelInfo, "Tunnel address %s, DNS %s", address, joinOrNone(dns))
			up = true
		} else if address != "" {
			w.mu.Lock()
			changed := w.socks != nil && (address != w.source || !slices.Equal(dns, w.dns))
			server := w.socks
			if changed {
				// openconnect reconnected on its own and the script reported a new address.
				w.source, w.dns = address, dns
			}
			w.mu.Unlock()
			if changed {
				server.Update(address, dns)
				w.logf(slog.LevelInfo, "Tunnel address now %s, DNS %s", address, joinOrNone(dns))
			}
		}
		d := time.Second
		if !up {
			d = 200 * time.Millisecond
		}
		if !sleepCtx(ctx, d) {
			return
		}
	}
}

func joinOrNone(dns []string) string {
	if len(dns) == 0 {
		return "(none)"
	}
	return strings.Join(dns, " ")
}

func (w *Tunnel) readState() map[string]string {
	path := w.StateFile()
	if path == "" {
		return map[string]string{}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return map[string]string{}
	}
	return ParseState(strings.ToValidUTF8(string(data), "\uFFFD"))
}

// Source returns the address and DNS servers the SOCKS server uses; ok is false while it is down.
func (w *Tunnel) Source() (string, []string, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.source, slices.Clone(w.dns), w.socks != nil
}

// WaitReady: ready means the SOCKS server is up. Polling the port like on POSIX would block:
// Windows takes about 2 s to refuse a connection to localhost.
func (w *Tunnel) WaitReady(timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if w.HasExited() {
			return false
		}
		w.mu.Lock()
		up := w.socks != nil
		w.mu.Unlock()
		if up {
			w.MarkReady()
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}

// Stop ends openconnect (Ctrl+C, killed after grace) and the SOCKS server.
func (w *Tunnel) Stop(grace time.Duration) {
	w.RemoveTokenFile()
	// Stopped while starting: Start ends the process as soon as it exists.
	if w.MarkStopping() {
		w.endProcess(grace)
	}
	w.stopServer()
}

// endProcess sends Ctrl+C for a clean logout and kills openconnect if it does not exit within grace.
func (w *Tunnel) endProcess(grace time.Duration) {
	pid := w.Pid()
	if pid == 0 || w.HasExited() {
		return
	}
	if w.CtrlC != nil && w.CtrlC(pid) {
		select {
		case <-w.Exited():
			return
		case <-time.After(grace):
		}
	}
	w.logf(slog.LevelWarn, "openconnect does not respond to Ctrl+C, terminating it")
	w.Kill()
	<-w.Exited()
}

func (w *Tunnel) stopServer() {
	w.mu.Lock()
	if w.cancel != nil {
		w.cancel()
	}
	server := w.socks
	w.socks = nil
	stateFile := w.stateFile
	w.mu.Unlock()
	if server != nil {
		server.Stop()
	}
	if stateFile != "" {
		_ = os.Remove(stateFile)
	}
}

// RemoveStaleStateFiles removes state files left behind by a crashed daemon. Returns how many.
func RemoveStaleStateFiles(directory string) int {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return 0
	}
	removed := 0
	for _, e := range entries {
		name := e.Name()
		if platform.IsWindows {
			name = strings.ToLower(name) // pathlib globs case-insensitively there
		}
		if ok, _ := filepath.Match(StatePrefix+"*.env", name); !ok {
			continue
		}
		if os.Remove(filepath.Join(directory, e.Name())) == nil {
			removed++
		}
	}
	return removed
}

// CtrlCHelperArg is the hidden first argument that makes the uni-vpn binary act as the Ctrl+C
// helper: "uni-vpn <CtrlCHelperArg> <pid>". main must call RunCtrlCHelper before anything else.
const CtrlCHelperArg = "__ctrl-c-helper"

// RunCtrlCHelper handles the helper invocation and exits; it returns for any other argv.
func RunCtrlCHelper(args []string) {
	if len(args) != 3 || args[1] != CtrlCHelperArg {
		return
	}
	pid, err := strconv.Atoi(args[2])
	if err != nil {
		os.Exit(1)
	}
	os.Exit(CtrlCHelperMain(pid))
}

// SendCtrlCVia runs argv plus pid as the Ctrl+C helper and reports whether it succeeded.
// A helper process does it, because attaching to another console would detach the daemon
// from its own.
func SendCtrlCVia(argv []string, pid int, timeout time.Duration) bool {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], append(argv[1:len(argv):len(argv)], strconv.Itoa(pid))...)
	platform.HideWindow(cmd)
	return cmd.Run() == nil
}

func passwordError(encoding string) string {
	return fmt.Sprintf("The password contains characters openconnect cannot read on this Windows (%s)", encoding)
}
