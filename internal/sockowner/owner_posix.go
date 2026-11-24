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
	other, _, err := owner(c)
	return other, err
}

// owner: OtherAccount, and whether the system named the end's account (see OtherAccountListens).
func owner(c net.Conn) (other, known bool, err error) {
	r, err := clientEnd(c)
	if err != nil {
		if broken(c) {
			return true, false, nil
		}
		return false, false, err
	}
	return otherOwner(r, self), r.known, nil
}

// listenerOther: whether the program that takes connections to 127.0.0.1:port is another
// account's, from the system's table; found is false when the table shows none.
func listenerOther(port int) (other, found bool, err error) {
	rows, err := table()
	if err != nil {
		return false, false, err
	}
	r, found := listenerOn(rows, port)
	return found && r.owner != self, found, nil
}

// ListenerOwner is the account (uid) of the program that takes connections to 127.0.0.1:port,
// from the system's table (the Mac's guard of the proxy rule, internal/guard); found is false
// when nothing listens there.
func ListenerOwner(port int) (uid uint32, found bool, err error) {
	rows, err := table()
	if err != nil {
		return 0, false, err
	}
	r, found := listenerOn(rows, port)
	return r.owner, found, nil
}
