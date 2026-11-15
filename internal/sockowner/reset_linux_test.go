//go:build linux && !android

package sockowner

import (
	"net"
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"testing"
)

// resetBy has python3, as account uid (nil: this one), send a request and reset the connection
// before uni-vpn looks it up: both ends leave the system's table at once, but the request is
// still there to read. It returns uni-vpn's end.
func resetBy(t *testing.T, uid *uint32) net.Conn {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("no python3")
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	script := "import socket, struct, sys; s = socket.create_connection(('127.0.0.1', int(sys.argv[1])));" +
		" s.sendall(b'POST /api/setup HTTP/1.1\\r\\n\\r\\n');" +
		" s.setsockopt(socket.SOL_SOCKET, socket.SO_LINGER, struct.pack('ii', 1, 0)); s.close()"
	cmd := exec.Command(python, "-c", script, strconv.Itoa(ln.Addr().(*net.TCPAddr).Port))
	if uid != nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: *uid, Gid: *uid}}
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatal(err, string(out))
	}
	server, err := ln.Accept()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { server.Close() })
	return server
}

// A reset connection does not get through as "cannot tell": nobody reads the answer.
func TestResetClientCountsAsAnotherAccount(t *testing.T) {
	if other, err := OtherAccount(resetBy(t, nil)); !other || err != nil {
		t.Fatal(other, err)
	}
}

// The same with a real other account (nobody): only root can start one, so most runs skip it.
func TestResetByAnotherAccountIsRefused(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skip("only root can connect as another account")
	}
	nobody := uint32(65534)
	if other, err := OtherAccount(resetBy(t, &nobody)); !other || err != nil {
		t.Fatal(other, err)
	}
}
