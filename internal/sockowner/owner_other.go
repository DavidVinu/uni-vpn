//go:build !(linux && !android) && !(darwin && !ios) && !windows

package sockowner

import "net"

// OtherAccount: a phone has one account, and its API answers only the app that holds the
// token (httpapi.Server.Token).
func OtherAccount(net.Conn) (bool, error) { return false, nil }

func owner(net.Conn) (other, known bool, err error) { return false, true, nil }
