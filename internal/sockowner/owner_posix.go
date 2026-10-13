//go:build (linux && !android) || (darwin && !ios)

package sockowner

import (
	"net"
	"os"
)

// self is this account.
var self = uint32(os.Getuid())

// OtherAccount reports whether the client's end of c, a connection accepted on 127.0.0.1,
// belongs to another account. An error means the system could not tell.
func OtherAccount(c net.Conn) (bool, error) {
	r, err := clientEnd(c)
	if err != nil {
		return false, err
	}
	return otherOwner(r, self), nil
}
