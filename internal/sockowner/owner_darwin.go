//go:build darwin && !ios

package sockowner

import (
	"net"

	"golang.org/x/sys/unix"
)

// clientEnd finds the client's end of c in the list netstat reads.
func clientEnd(c net.Conn) (row, error) {
	client, server, err := ends(c)
	if err != nil {
		return row{}, err
	}
	rows, err := table()
	if err != nil {
		return row{}, err
	}
	return find(rows, client, server, self)
}

// table is every TCP socket netstat lists, listeners included.
func table() ([]row, error) {
	buf, err := unix.SysctlRaw("net.inet.tcp.pcblist_n")
	if err != nil {
		return nil, err
	}
	return parsePCBList(buf), nil
}
