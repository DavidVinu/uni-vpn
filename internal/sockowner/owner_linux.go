//go:build linux && !android

package sockowner

import (
	"encoding/binary"
	"net"
	"os"
)

// clientEnd finds the client's end of c in /proc/net/tcp, or tcp6 for a dual-stack client
// (an IPv6 socket to ::ffff:127.0.0.1). Without IPv6 there is no tcp6.
func clientEnd(c net.Conn) (row, error) {
	client, server, err := ends(c)
	if err != nil {
		return row{}, err
	}
	var rows []row
	for _, name := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		data, err := os.ReadFile(name)
		if os.IsNotExist(err) && name == "/proc/net/tcp6" {
			break
		}
		if err != nil {
			return row{}, err
		}
		more, err := parseProcNet(data, binary.NativeEndian)
		if err != nil {
			return row{}, err
		}
		rows = append(rows, more...)
	}
	return find(rows, client, server, self)
}
