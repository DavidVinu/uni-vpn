package socks

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math/rand/v2"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"time"
)

// DNSError is a failed lookup (DnsError in Python).
type DNSError struct{ msg string }

func (e *DNSError) Error() string { return e.msg }

func dnsErr(format string, args ...any) *DNSError { return &DNSError{fmt.Sprintf(format, args...)} }

// BuildQuery builds a DNS query for the A record of name (recursion desired).
// Non-ASCII labels are rejected: Python's IDNA codec would convert them, but SOCKS clients
// only ever send ASCII or (after decoding) U+FFFD, which nameprep prohibits as well.
func BuildQuery(name string, qid uint16) ([]byte, error) {
	q := make([]byte, 12, 12+len(name)+6)
	binary.BigEndian.PutUint16(q[0:], qid)
	binary.BigEndian.PutUint16(q[2:], 0x0100) // recursion desired, one question
	binary.BigEndian.PutUint16(q[4:], 1)
	for _, label := range strings.Split(strings.TrimRight(name, "."), ".") {
		if label == "" || len(label) > 63 || !isASCII(label) {
			return nil, dnsErr("invalid host name: %s", name)
		}
		q = append(q, byte(len(label)))
		q = append(q, label...)
	}
	return append(q, 0, 0, 1, 0, 1), nil // type A, class IN
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

func skipName(data []byte, offset int) (int, error) {
	for {
		if offset >= len(data) {
			return 0, dnsErr("truncated name")
		}
		length := int(data[offset])
		if length&0xC0 == 0xC0 {
			return offset + 2, nil
		}
		if length == 0 {
			return offset + 1, nil
		}
		offset += 1 + length
	}
}

// ParseResponse returns the IPv4 addresses from the answer section, in order. CNAME
// records are skipped.
func ParseResponse(data []byte, qid uint16) ([]string, error) {
	if len(data) < 12 {
		return nil, dnsErr("short response")
	}
	rid := binary.BigEndian.Uint16(data[0:])
	flags := binary.BigEndian.Uint16(data[2:])
	qdcount := int(binary.BigEndian.Uint16(data[4:]))
	ancount := int(binary.BigEndian.Uint16(data[6:]))
	if rid != qid {
		return nil, dnsErr("response id mismatch")
	}
	rcode := flags & 0x0F
	if rcode == 3 {
		return nil, dnsErr("host not found")
	}
	if rcode != 0 {
		return nil, dnsErr("DNS error %d", rcode)
	}
	offset := 12
	var err error
	for range qdcount {
		if offset, err = skipName(data, offset); err != nil {
			return nil, err
		}
		offset += 4
	}
	addresses := []string{}
	for range ancount {
		if offset, err = skipName(data, offset); err != nil {
			return nil, err
		}
		if offset+10 > len(data) {
			return nil, dnsErr("truncated answer")
		}
		rtype := binary.BigEndian.Uint16(data[offset:])
		rclass := binary.BigEndian.Uint16(data[offset+2:])
		rdlength := int(binary.BigEndian.Uint16(data[offset+8:]))
		offset += 10
		if offset+rdlength > len(data) {
			return nil, dnsErr("truncated answer")
		}
		if rtype == 1 && rclass == 1 && rdlength == 4 {
			addresses = append(addresses, netip.AddrFrom4([4]byte(data[offset:offset+4])).String())
		}
		offset += rdlength
	}
	return addresses, nil
}

// parseIPv4 accepts what Python's ipaddress.IPv4Address accepts from a string.
func parseIPv4(s string) (string, bool) {
	parts := strings.Split(s, ".")
	if len(parts) != 4 {
		return "", false
	}
	for _, p := range parts {
		if p == "" || len(p) > 3 {
			return "", false
		}
		for i := 0; i < len(p); i++ {
			if p[i] < '0' || p[i] > '9' {
				return "", false
			}
		}
		if p != "0" && p[0] == '0' {
			return "", false
		}
		if n, _ := strconv.Atoi(p); n > 255 {
			return "", false
		}
	}
	return s, true
}

// Resolve returns the first IPv4 address of name, asked from each server in turn over UDP
// from source. Python's defaults are a 3 s timeout and port 53.
func Resolve(ctx context.Context, name string, servers []string, source string, timeout time.Duration, port int) (string, error) {
	if ip, ok := parseIPv4(name); ok {
		return ip, nil
	}
	if len(servers) == 0 {
		return "", dnsErr("no DNS server from the VPN")
	}
	var last error = dnsErr("no answer")
	for _, server := range servers {
		qid := uint16(rand.IntN(1 << 16))
		conn, err := dialUDP(ctx, source, server, port)
		if err != nil {
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			last = err
			continue
		}
		address, err := exchange(ctx, conn, name, qid, timeout)
		conn.Close()
		if err == nil {
			return address, nil
		}
		if ctx.Err() != nil {
			return "", err
		}
		last = err
		var de *DNSError
		if errors.As(err, &de) && de.msg == "host not found" {
			break
		}
	}
	return "", dnsErr("%s: %s", name, reason(last))
}

// reason is the text of err for the log; a timeout has none of its own.
func reason(err error) string {
	var de *DNSError
	if errors.As(err, &de) {
		return de.msg
	}
	if isTimeout(err) {
		return "timed out"
	}
	if text := osErrorString(err); text != "" {
		return text
	}
	return fmt.Sprintf("%T", err)
}

func dialUDP(ctx context.Context, source, server string, port int) (*net.UDPConn, error) {
	src, srcErr := netip.ParseAddr(source)
	if dst, err := netip.ParseAddr(server); err == nil && srcErr == nil && src.Is4() != dst.Is4() {
		// A server address of another family (ValueError in Python).
		return nil, dnsErr("unusable DNS server %s", server)
	}
	laddr, err := net.ResolveUDPAddr("udp", net.JoinHostPort(source, "0"))
	if err != nil {
		return nil, err
	}
	d := net.Dialer{LocalAddr: laddr}
	c, err := d.DialContext(ctx, "udp", net.JoinHostPort(server, strconv.Itoa(port)))
	if err != nil {
		var ae *net.AddrError
		if errors.As(err, &ae) && ae.Err == "no suitable address found" {
			return nil, dnsErr("unusable DNS server %s", server)
		}
		return nil, err
	}
	conn := c.(*net.UDPConn)
	reportICMPErrors(conn)
	return conn, nil
}

func exchange(ctx context.Context, conn *net.UDPConn, name string, qid uint16, timeout time.Duration) (string, error) {
	query, err := BuildQuery(name, qid)
	if err != nil {
		return "", err
	}
	stop := context.AfterFunc(ctx, func() { conn.SetReadDeadline(time.Unix(1, 0)) })
	defer stop()
	if _, err := conn.Write(query); err != nil {
		return "", err
	}
	conn.SetReadDeadline(time.Now().Add(timeout))
	buf := make([]byte, 65536)
	n, err := conn.Read(buf)
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if err != nil {
		return "", err
	}
	addresses, err := ParseResponse(buf[:n], qid)
	if err != nil {
		return "", err
	}
	if len(addresses) == 0 {
		return "", dnsErr("no IPv4 address")
	}
	return addresses[0], nil
}
