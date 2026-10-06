package httpapi

import "testing"

func TestAllowedOrigin(t *testing.T) {
	if !AllowedOrigin("", false, 1081) || !AllowedOrigin("http://127.0.0.1:1081", true, 1081) ||
		!AllowedOrigin("http://localhost:1081", true, 1081) {
		t.Fatal("own origins refused")
	}
	// Without the extension there is no longer any foreign origin allowed to POST. Sandboxed
	// iframes and data: pages send "null": not a trustworthy origin.
	for _, origin := range []string{"http://localhost:9999", "chrome-extension://abcdef", "moz-extension://1234-5678",
		"https://evil.example", "http://127.0.0.1:9999", "null", "chrome-extension://abc\nX-Injected: 1",
		"chrome-extension://abc\r\n", "moz-extension://abc/def", "chrome-extension://a b", "chrome-extension://",
		"xchrome-extension://abc"} {
		if AllowedOrigin(origin, true, 1081) {
			t.Fatal(origin)
		}
	}
}

func TestAllowedHost(t *testing.T) {
	for _, host := range []string{"127.0.0.1", "127.0.0.1:1081", "localhost", "localhost:1081"} {
		if !AllowedHost(host, true, 1081) {
			t.Fatal(host)
		}
	}
	for _, host := range []string{"evil.example:1081", "127.0.0.1:9", "", "LOCALHOST"} {
		if AllowedHost(host, true, 1081) {
			t.Fatal(host)
		}
	}
	if AllowedHost("localhost", false, 1081) {
		t.Fatal("missing Host header accepted")
	}
}

func TestPyStrip(t *testing.T) {
	if got := PyStrip("\x1c  ab c　\t"); got != "ab c" {
		t.Fatalf("%q", got)
	}
}
