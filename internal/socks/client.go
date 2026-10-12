package socks

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"strconv"
	"time"
)

// Dial opens a connection to address (host:port) through the SOCKS5 proxy at proxy. The proxy
// resolves the name, so names only the VPN's DNS knows work too.
func Dial(ctx context.Context, proxy, address string) (net.Conn, error) {
	host, portText, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port <= 0 || port > 65535 || len(host) == 0 || len(host) > 255 {
		return nil, fmt.Errorf("socks: bad address %q", address)
	}
	var d net.Dialer
	c, err := d.DialContext(ctx, "tcp", proxy)
	if err != nil {
		return nil, err
	}
	if deadline, ok := ctx.Deadline(); ok {
		c.SetDeadline(deadline)
	}
	stop := context.AfterFunc(ctx, func() { c.Close() })
	err = handshake(c, host, port)
	if !stop() {
		err = ctx.Err()
	}
	if err != nil {
		c.Close()
		return nil, err
	}
	c.SetDeadline(time.Time{})
	return c, nil
}

func handshake(c net.Conn, host string, port int) error {
	if _, err := c.Write([]byte{5, 1, 0}); err != nil {
		return err
	}
	var reply [4]byte
	if _, err := io.ReadFull(c, reply[:2]); err != nil {
		return err
	}
	if reply[0] != 5 || reply[1] != 0 {
		return fmt.Errorf("socks: proxy refused the greeting")
	}
	req := []byte{5, 1, 0, 3, byte(len(host))}
	req = binary.BigEndian.AppendUint16(append(req, host...), uint16(port))
	if _, err := c.Write(req); err != nil {
		return err
	}
	if _, err := io.ReadFull(c, reply[:]); err != nil {
		return err
	}
	if reply[1] != RepOK {
		return fmt.Errorf("socks: %s not reachable (reply %d)", host, reply[1])
	}
	skip := 0
	switch reply[3] {
	case 1:
		skip = 4
	case 4:
		skip = 16
	case 3:
		var n [1]byte
		if _, err := io.ReadFull(c, n[:]); err != nil {
			return err
		}
		skip = int(n[0])
	default:
		return fmt.Errorf("socks: bad reply")
	}
	_, err := io.ReadFull(c, make([]byte, skip+2))
	return err
}
