package socks

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"log/slog"
	"net"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func answer(query []byte, addresses []string, rcode uint16, cname bool) []byte {
	var records []byte
	count := 0
	rr := func(rtype uint16, data []byte) {
		records = append(records, 0xc0, 0x0c)
		records = binary.BigEndian.AppendUint16(records, rtype)
		records = binary.BigEndian.AppendUint16(records, 1)
		records = binary.BigEndian.AppendUint32(records, 60)
		records = binary.BigEndian.AppendUint16(records, uint16(len(data)))
		records = append(records, data...)
		count++
	}
	if cname {
		rr(5, []byte("\x05alias\x07example\x00"))
	}
	for _, a := range addresses {
		rr(1, net.ParseIP(a).To4())
	}
	out := append([]byte{}, query[:2]...)
	for _, v := range []uint16{0x8180 | rcode, 1, uint16(count), 0, 0} {
		out = binary.BigEndian.AppendUint16(out, v)
	}
	out = append(out, query[12:]...)
	return append(out, records...)
}

type fakeDNS struct {
	conn    *net.UDPConn
	table   map[string]string
	mu      sync.Mutex
	queries []string
}

func startFakeDNS(t *testing.T, table map[string]string) *fakeDNS {
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeDNS{conn: conn, table: table}
	go func() {
		buf := make([]byte, 2048)
		for {
			n, addr, err := conn.ReadFromUDP(buf)
			if err != nil {
				return
			}
			data := buf[:n]
			var name []string
			for off := 12; data[off] != 0; off += 1 + int(data[off]) {
				name = append(name, string(data[off+1:off+1+int(data[off])]))
			}
			host := strings.Join(name, ".")
			f.mu.Lock()
			f.queries = append(f.queries, host)
			f.mu.Unlock()
			if ip, ok := table[host]; ok {
				conn.WriteToUDP(answer(data, []string{ip}, 0, true), addr)
			} else {
				conn.WriteToUDP(answer(data, nil, 3, false), addr)
			}
		}
	}()
	t.Cleanup(func() { conn.Close() })
	return f
}

func (f *fakeDNS) port() int { return f.conn.LocalAddr().(*net.UDPAddr).Port }

func (f *fakeDNS) seen() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string{}, f.queries...)
}

func TestQueryRoundtrip(t *testing.T) {
	query, err := BuildQuery("sogo.uni-heidelberg.de", 0x1234)
	if err != nil || !bytes.Equal(query[:2], []byte{0x12, 0x34}) {
		t.Fatal(query, err)
	}
	got, err := ParseResponse(answer(query, []string{"10.1.2.3"}, 0, true), 0x1234)
	if err != nil || !reflect.DeepEqual(got, []string{"10.1.2.3"}) {
		t.Fatal(got, err)
	}
	want := []byte("\x12\x34\x01\x00\x00\x01\x00\x00\x00\x00\x00\x00\x04sogo\x0euni-heidelberg\x02de\x00\x00\x01\x00\x01")
	if !bytes.Equal(query, want) {
		t.Fatalf("%q", query)
	}
}

func TestNXDomainAndMismatch(t *testing.T) {
	query, _ := BuildQuery("x.example", 7)
	var de *DNSError
	if _, err := ParseResponse(answer(query, nil, 3, false), 7); !errors.As(err, &de) || !strings.Contains(err.Error(), "not found") {
		t.Fatal(err)
	}
	if _, err := ParseResponse(answer(query, []string{"1.2.3.4"}, 0, false), 8); !errors.As(err, &de) || !strings.Contains(err.Error(), "mismatch") {
		t.Fatal(err)
	}
	if _, err := ParseResponse(answer(query, nil, 2, false), 7); err == nil || err.Error() != "DNS error 2" {
		t.Fatal(err)
	}
}

func TestBadNames(t *testing.T) {
	for _, name := range []string{"", "a..b", strings.Repeat("x", 64) + ".de", ".", "\ufffd.de"} {
		var de *DNSError
		if _, err := BuildQuery(name, 1); !errors.As(err, &de) || err.Error() != "invalid host name: "+name {
			t.Errorf("%q: %v", name, err)
		}
	}
	if _, err := BuildQuery("example.de..", 1); err != nil {
		t.Error(err)
	}
}

func TestTruncated(t *testing.T) {
	var de *DNSError
	if _, err := ParseResponse([]byte{0, 1}, 1); !errors.As(err, &de) {
		t.Fatal(err)
	}
	query, _ := BuildQuery("x.example", 7)
	full := answer(query, []string{"1.2.3.4"}, 0, false)
	if _, err := ParseResponse(full[:len(full)-5], 7); err == nil || err.Error() != "truncated answer" {
		t.Fatal(err)
	}
	// Python raises a ValueError from ipaddress here, not a DnsError.
	_, err := ParseResponse(full[:len(full)-2], 7)
	var ve *valueError
	if !errors.As(err, &ve) || err.Error() != `b'\x01\x02' (len 2 != 4) is not permitted as an IPv4 address` {
		t.Fatal(err)
	}
}

func TestParseIPv4LikePython(t *testing.T) {
	for in, ok := range map[string]bool{"1.2.3.4": true, "0.0.0.0": true, "255.255.255.255": true,
		"01.2.3.4": false, "256.1.1.1": false, "1.2.3": false, "1.2.3.4.5": false, "a.b.c.d": false,
		"1.2.3.-4": false, "": false, "1.2.3.4 ": false} {
		if _, got := parseIPv4(in); got != ok {
			t.Errorf("%q: %v", in, got)
		}
	}
}

func TestResolveErrors(t *testing.T) {
	ctx := context.Background()
	if _, err := Resolve(ctx, "x.example", nil, "127.0.0.1", time.Second, 53); err == nil || err.Error() != "no DNS server from the VPN" {
		t.Fatal(err)
	}
	dns := startFakeDNS(t, map[string]string{})
	_, err := Resolve(ctx, "x.example", []string{"::1", "127.0.0.1"}, "127.0.0.1", time.Second, dns.port())
	if err == nil || err.Error() != "x.example: host not found" {
		t.Fatal(err)
	}
	_, err = Resolve(ctx, "x.example", []string{"::1"}, "127.0.0.1", time.Second, dns.port())
	if err == nil || err.Error() != "x.example: unusable DNS server ::1" {
		t.Fatal(err)
	}
	// A server that never answers: Python prints the empty TimeoutError text.
	silent, _ := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	defer silent.Close()
	_, err = Resolve(ctx, "x.example", []string{"127.0.0.1"}, "127.0.0.1", 100*time.Millisecond,
		silent.LocalAddr().(*net.UDPAddr).Port)
	if err == nil || err.Error() != "x.example: " {
		t.Fatalf("%q", err)
	}
	if got, err := Resolve(ctx, "10.0.0.1", nil, "127.0.0.1", time.Second, 53); got != "10.0.0.1" || err != nil {
		t.Fatal(got, err)
	}
}

type env struct {
	dns      *fakeDNS
	echoPort int
	server   *Server
}

func freePort(t *testing.T) int {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

func setup(t *testing.T) *env {
	e := &env{dns: startFakeDNS(t, map[string]string{"intra.example": "127.0.0.1"})}
	echo, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			c, err := echo.Accept()
			if err != nil {
				return
			}
			go func() { io.Copy(c, c); c.Close() }()
		}
	}()
	t.Cleanup(func() { echo.Close() })
	e.echoPort = echo.Addr().(*net.TCPAddr).Port
	e.server = NewServer(freePort(t), "127.0.0.1", []string{"127.0.0.1"}, slog.New(slog.DiscardHandler))
	e.server.DNSPort = e.dns.port()
	e.server.ConnectTimeout = 5 * time.Second
	if err := e.server.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(e.server.Stop)
	return e
}

func (e *env) dial(t *testing.T) net.Conn {
	c, err := net.Dial("tcp", "127.0.0.1:"+strconv.Itoa(e.server.Port))
	if err != nil {
		t.Fatal(err)
	}
	c.SetDeadline(time.Now().Add(5 * time.Second))
	t.Cleanup(func() { c.Close() })
	return c
}

func readN(t *testing.T, c net.Conn, n int) []byte {
	t.Helper()
	buf := make([]byte, n)
	if _, err := io.ReadFull(c, buf); err != nil {
		t.Fatal(err)
	}
	return buf
}

func (e *env) connect(t *testing.T, atyp byte, address []byte, port int) (net.Conn, byte) {
	c := e.dial(t)
	c.Write([]byte{5, 1, 0})
	if got := readN(t, c, 2); !bytes.Equal(got, []byte{5, 0}) {
		t.Fatal(got)
	}
	req := append([]byte{5, 1, 0, atyp}, address...)
	c.Write(binary.BigEndian.AppendUint16(req, uint16(port)))
	return c, readN(t, c, 10)[1]
}

func TestConnectByNameResolvesViaVPNDNS(t *testing.T) {
	e := setup(t)
	name := "intra.example"
	c, code := e.connect(t, 3, append([]byte{byte(len(name))}, name...), e.echoPort)
	if code != RepOK {
		t.Fatal(code)
	}
	c.Write([]byte("hello"))
	if got := readN(t, c, 5); string(got) != "hello" {
		t.Fatal(got)
	}
	if got := e.dns.seen(); !reflect.DeepEqual(got, []string{"intra.example"}) {
		t.Fatal(got)
	}
}

func TestConnectByIPv4(t *testing.T) {
	e := setup(t)
	c, code := e.connect(t, 1, []byte{127, 0, 0, 1}, e.echoPort)
	if code != RepOK {
		t.Fatal(code)
	}
	c.Write([]byte("x"))
	if got := readN(t, c, 1); string(got) != "x" {
		t.Fatal(got)
	}
	if got := e.dns.seen(); len(got) != 0 {
		t.Fatal(got)
	}
}

func TestHalfCloseIsPassedOn(t *testing.T) {
	e := setup(t)
	c, code := e.connect(t, 1, []byte{127, 0, 0, 1}, e.echoPort)
	if code != RepOK {
		t.Fatal(code)
	}
	c.Write([]byte("abc"))
	c.(*net.TCPConn).CloseWrite()
	got, err := io.ReadAll(c)
	if err != nil || string(got) != "abc" {
		t.Fatal(got, err)
	}
}

func TestUnknownHost(t *testing.T) {
	e := setup(t)
	name := "nope.example"
	if _, code := e.connect(t, 3, append([]byte{byte(len(name))}, name...), e.echoPort); code != RepHostUnreachable {
		t.Fatal(code)
	}
}

func TestRefused(t *testing.T) {
	e := setup(t)
	if _, code := e.connect(t, 1, []byte{127, 0, 0, 1}, freePort(t)); code != RepRefused {
		t.Fatal(code)
	}
}

func TestIPv6Unsupported(t *testing.T) {
	e := setup(t)
	if _, code := e.connect(t, 4, make([]byte, 16), 80); code != RepAtypUnsupported {
		t.Fatal(code)
	}
}

func TestAuthRequiredIsRefused(t *testing.T) {
	e := setup(t)
	c := e.dial(t)
	c.Write([]byte{5, 1, 2})
	if got := readN(t, c, 2); !bytes.Equal(got, []byte{5, 0xff}) {
		t.Fatal(got)
	}
}

func TestBindCommandUnsupported(t *testing.T) {
	e := setup(t)
	c := e.dial(t)
	c.Write([]byte{5, 1, 0, 5, 2, 0, 1, 127, 0, 0, 1, 0, 0x50})
	if got := readN(t, c, 2); !bytes.Equal(got, []byte{5, 0}) {
		t.Fatal(got)
	}
	if got := readN(t, c, 10); got[1] != RepCmdUnsupported {
		t.Fatal(got)
	}
}

func TestWrongVersionIsDropped(t *testing.T) {
	e := setup(t)
	c := e.dial(t)
	c.Write([]byte{4, 1, 0})
	if n, err := c.Read(make([]byte, 1)); n != 0 || err == nil {
		t.Fatal(n, err)
	}
}

func TestStopClosesConnections(t *testing.T) {
	e := setup(t)
	c, code := e.connect(t, 1, []byte{127, 0, 0, 1}, e.echoPort)
	if code != RepOK {
		t.Fatal(code)
	}
	e.server.Stop()
	if n, err := c.Read(make([]byte, 1)); n != 0 || err == nil {
		t.Fatal(n, err)
	}
	if e.server.Addr() != nil {
		t.Fatal("still listening")
	}
}

func TestSetRoute(t *testing.T) {
	s := NewServer(0, "10.0.0.1", []string{"10.0.0.53"}, nil)
	s.SetRoute("10.0.0.2", []string{"10.0.0.54"})
	if src, dns := s.Route(); src != "10.0.0.2" || !reflect.DeepEqual(dns, []string{"10.0.0.54"}) {
		t.Fatal(src, dns)
	}
}
