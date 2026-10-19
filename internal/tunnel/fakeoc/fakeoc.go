// Package fakeoc is a stand-in for openconnect in tests, a port of tests/fake_openconnect.py.
// Test binaries call MaybeRun from TestMain and pass their own executable as openconnect;
// with EnvVar set the binary then behaves like the fake instead of running tests.
//
// Environment variables:
//
//	FAKE_MODE           ok (default) | auth_fail | input_required | totp_rejected | totp_replayed | never_ready |
//	                    ignore_sigterm | exit_after_ready | clientcert_auth_fail | gateway_503 | cstp_503 |
//	                    second_password
//	FAKE_DELAY          seconds until the port listens (default 0.2)
//	FAKE_EXIT_AFTER     with exit_after_ready: seconds after becoming ready (default 0.5)
//	FAKE_PASSWORD_FILE  file every line read from stdin is appended to (password, "push" for Duo,
//	                    a typed code); without --non-inter a refused password is asked for again
//	FAKE_ARGS_FILE      file the command line is appended to, one JSON list per start
//	FAKE_TOKEN_FILE     file the contents of --token-secret=@file are appended to
//	                    (read shortly before becoming ready, like openconnect when generating the code)
//	FAKE_SCRIPT_ERROR   with UNI_VPN_STATE: an ERROR line the vpnc script reports
package fakeoc

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf16"
)

// EnvVar selects the role of the test binary: "openconnect" or "portholder".
const EnvVar = "UNI_VPN_FAKE_ROLE"

// MaybeRun runs the fake and exits when EnvVar asks for it; otherwise it returns.
func MaybeRun() {
	switch os.Getenv(EnvVar) {
	case "openconnect":
		os.Exit(openconnect(os.Args[1:]))
	case "portholder":
		os.Exit(portHolder(os.Args[1:]))
	}
}

func log(text string) {
	os.Stderr.WriteString(text + "\n")
}

func echo(conn net.Conn) {
	defer conn.Close()
	_, _ = io.Copy(conn, conn)
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func seconds(name, def string) time.Duration {
	v := os.Getenv(name)
	if v == "" {
		v = def
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		panic(err)
	}
	return time.Duration(f * float64(time.Second))
}

func appendFile(path, text string) {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o666)
	if err != nil {
		panic(err)
	}
	defer f.Close()
	if _, err := f.WriteString(text); err != nil {
		panic(err)
	}
}

// jsonList encodes like Python's json.dumps of a list of strings.
func jsonList(items []string) string {
	var b strings.Builder
	b.WriteByte('[')
	for i, s := range items {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteByte('"')
		for _, r := range s {
			switch r {
			case '"':
				b.WriteString(`\"`)
			case '\\':
				b.WriteString(`\\`)
			case '\n':
				b.WriteString(`\n`)
			case '\r':
				b.WriteString(`\r`)
			case '\t':
				b.WriteString(`\t`)
			case '\b':
				b.WriteString(`\b`)
			case '\f':
				b.WriteString(`\f`)
			default:
				switch {
				case r < 0x20 || (r >= 0x7f && r < 0x10000):
					fmt.Fprintf(&b, `\u%04x`, r)
				case r >= 0x10000:
					r1, r2 := utf16.EncodeRune(r)
					fmt.Fprintf(&b, `\u%04x\u%04x`, r1, r2)
				default:
					b.WriteRune(r)
				}
			}
		}
		b.WriteByte('"')
	}
	b.WriteByte(']')
	return b.String()
}

func openconnect(args []string) int {
	port := 0
	tokenPath := ""
	for _, arg := range args {
		if strings.HasPrefix(arg, "--script=") {
			if f := strings.Fields(arg); isDigits(f[len(f)-1]) {
				port, _ = strconv.Atoi(f[len(f)-1])
			}
		}
		if p, ok := strings.CutPrefix(arg, "--token-secret=@"); ok {
			tokenPath = p
		}
	}
	mode := os.Getenv("FAKE_MODE")
	if mode == "" {
		mode = "ok"
	}
	if slices.Contains(args, "--help") && os.Getenv("FAKE_VERSION") != "9.01" {
		os.Stdout.WriteString("Usage:  openconnect [options] <server>\n      --no-external-auth          Do not offer or use auth methods requiring external browser\n")
		return 1
	}
	if os.Getenv("FAKE_VERSION") == "9.01" {
		// openconnect before 9.10 (Debian 12: 9.01, Ubuntu 22.04: 8.20) has no --no-external-auth:
		// getopt's complaint, then usage() on stdout and exit(1).
		usage := "Usage:  openconnect [options] <server>\nOpen client for multiple VPN protocols, version v9.01\n" +
			"      --external-browser=BROWSER  Set external browser executable\n"
		if slices.Contains(args, "--help") {
			os.Stdout.WriteString(usage)
			return 1
		}
		if slices.Contains(args, "--no-external-auth") {
			log("openconnect: unrecognized option '--no-external-auth'")
			os.Stdout.WriteString(usage)
			return 1
		}
	}
	if mode == "totp_replayed" {
		// Another device of the account used this window's code (LinOTP takes each code once):
		// only the first start is rejected. FAKE_ARGS_FILE counts the starts.
		mode = "ok"
		if data, _ := os.ReadFile(os.Getenv("FAKE_ARGS_FILE")); len(data) == 0 {
			mode = "totp_rejected"
		}
	}
	// Before the password is recorded: a test that has seen the password line knows the
	// signals are handled (with -race the binary takes a while to get here).
	sigs := make(chan os.Signal, 4)
	signal.Notify(sigs, append([]os.Signal{syscall.SIGTERM, os.Interrupt}, extraSignals...)...)
	go func() {
		for s := range sigs {
			if isReconnectSignal(s) {
				log("SIGUSR2 received")
				continue
			}
			if mode == "ignore_sigterm" {
				log("SIGTERM ignored")
				continue
			}
			log("User cancelled (SIGINT/SIGTERM); exiting.")
			os.Exit(0)
		}
	}()

	if f := os.Getenv("FAKE_ARGS_FILE"); f != "" {
		appendFile(f, jsonList(args)+"\n")
	}
	// Like openconnect's prompts: line by line. Without --non-inter it waits for the next line
	// (Duo's "push", a typed code) and reads until uni-vpn closes stdin.
	stdin := bufio.NewReader(os.Stdin)
	next := func() bool {
		line, err := stdin.ReadString('\n')
		if line == "" && err != nil {
			return false
		}
		if f := os.Getenv("FAKE_PASSWORD_FILE"); f != "" {
			appendFile(f, strings.TrimRight(line, "\r\n")+"\n")
		}
		return true
	}
	interactive := !slices.Contains(args, "--non-inter")
	prompt := func(label string) bool {
		os.Stderr.WriteString(label)
		ok := next()
		os.Stderr.WriteString("\n")
		return ok
	}
	next()
	if !interactive {
		for next() {
		}
	}
	log("POST https://fake.example/")
	delay := seconds("FAKE_DELAY", "0.2")

	switch mode {
	case "auth_fail":
		// Wrong password (measured 2026-09-08): "Login failed." comes before any OTP prompt,
		// then the server shows the form again and openconnect has no password left.
		time.Sleep(delay)
		log("Bitte geben Sie ihren Benutzernamen und ihr Passwort ein.")
		log("Login failed.")
		// openconnect's prompt ends without a newline.
		for interactive && prompt("Password:") {
			log("Bitte geben Sie ihren Benutzernamen und ihr Passwort ein.")
			log("Login failed.")
		}
		log("Bitte geben Sie ihren Benutzernamen und ihr Passwort ein.")
		log("Password:")
		log("***")
		log("User input required in non-interactive mode")
		log("Failed to complete authentication")
		return 1
	case "second_password":
		// Duo's field in the password's form: openconnect asks for it before sending the form.
		log("Please enter your username and password.")
		if !prompt("Second Password:") {
			log("fgets (stdin): Inappropriate ioctl for device")
			return 1
		}
		interactive = false
	case "clientcert_auth_fail":
		// An ASA that asks for an optional client certificate (auth.c, PRG_INFO), then the
		// wrong-password sequence measured on 2026-09-08.
		log("Connected to HTTPS on fake.example with ciphersuite (TLS1.3)-(ECDHE-SECP256R1)-(AES-256-GCM)")
		log("Server requested SSL client certificate; none was configured")
		log("POST https://fake.example/")
		log("XML POST enabled")
		time.Sleep(delay)
		log("Please enter your username and password.")
		log("Login failed.")
		log("Please enter your username and password.")
		log("User input required in non-interactive mode")
		log("Failed to complete authentication")
		return 1
	case "gateway_503":
		// Real openconnect 9.21 when the gateway answers the login POST with an HTTP error
		// (http.c "Unexpected %d result from server", main.c "Failed to complete authentication").
		log("Connected to 192.0.2.1:443")
		log("SSL negotiation with fake.example")
		log("Connected to HTTPS on fake.example with ciphersuite (TLS1.3)-(ECDHE-SECP256R1)-(AES-256-GCM)")
		log("Unexpected 503 result from server")
		log("Failed to complete authentication")
		return 1
	case "cstp_503":
		// Login accepted, then the tunnel CONNECT is refused (cstp.c), main.c exits 1.
		log("Bitte geben Sie ihren Benutzernamen und ihr Passwort ein.")
		log("POST https://fake.example/")
		log("Got inappropriate HTTP CONNECT response: HTTP/1.1 503 Service Unavailable")
		log("Creating SSL connection failed")
		log("Unknown error; exiting.")
		return 1
	case "input_required":
		log("User input required in non-interactive mode")
		log("Failed to complete authentication")
		return 1
	case "totp_rejected":
		// Wrong secret (measured 2026-09-08): the password was accepted, the code rejected;
		// the server sends no second OTP form but starts over.
		log("Bitte geben Sie ihren Benutzernamen und ihr Passwort ein.")
		log("Bitte zweiten Faktor eingeben (OTP) / Please enter second factor (OTP).")
		if tokenPath != "" || !interactive || !next() {
			log("Generating OATH TOTP token code")
		}
		log("Login failed.")
		for interactive && next() {
			log("Bitte geben Sie ihren Benutzernamen und ihr Passwort ein.")
			log("Login failed.")
		}
		log("Bitte geben Sie ihren Benutzernamen und ihr Passwort ein.")
		log("Password:")
		log("***")
		log("User input required in non-interactive mode")
		log("Failed to complete authentication")
		return 1
	case "never_ready":
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, syscall.SIGTERM)
		select {
		case <-ch:
			return 0
		case <-time.After(time.Hour):
			return 0
		}
	}

	time.Sleep(delay)
	if tokenPath != "" {
		// Like the real openconnect when logging in with --token-mode=totp (measured 2026-09-08).
		log("Bitte zweiten Faktor eingeben (OTP) / Please enter second factor (OTP).")
		log("Generating OATH TOTP token code")
	} else if interactive {
		// The second prompt: the OTP form for a typed code, Duo's "push".
		log("Bitte zweiten Faktor eingeben (OTP) / Please enter second factor (OTP).")
		for next() {
		}
	}
	if out := os.Getenv("FAKE_TOKEN_FILE"); tokenPath != "" && out != "" {
		// openconnect reads the file only when generating the code, i.e. after starting.
		data, err := os.ReadFile(tokenPath)
		if err != nil {
			panic(err)
		}
		appendFile(out, strings.TrimRight(string(data), "\n")+"\n")
	}
	if state := os.Getenv("UNI_VPN_STATE"); state != "" {
		// Windows: openconnect runs the vpnc-script, which reports the tunnel address.
		text := ""
		if e := os.Getenv("FAKE_SCRIPT_ERROR"); e != "" {
			text += "ERROR=" + e + "\n"
		}
		text += "INTERNAL_IP4_ADDRESS=127.0.0.1\nINTERNAL_IP4_DNS=127.0.0.1\nTUNIDX=7\n"
		if err := os.WriteFile(state, []byte(text), 0o666); err != nil {
			panic(err)
		}
		log("Connected as 127.0.0.1, using SSL")
		select {}
	}
	server, err := net.Listen("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		panic(err)
	}
	log("Connected as 10.0.0.2, using SSL")
	if mode == "exit_after_ready" {
		go func() {
			time.Sleep(seconds("FAKE_EXIT_AFTER", "0.5"))
			log("Session terminated by server")
			os.Exit(1)
		}()
	}
	for {
		conn, err := server.Accept()
		if err != nil {
			continue
		}
		go echo(conn)
	}
}

// portHolder stands in for a leftover ocproxy: argv "ocproxy -D 127.0.0.1:PORT -k 30".
func portHolder(args []string) int {
	addr := args[2]
	l, err := net.Listen("tcp4", addr)
	if err != nil {
		return 3
	}
	for {
		c, err := l.Accept()
		if err == nil {
			c.Close()
		}
	}
}
