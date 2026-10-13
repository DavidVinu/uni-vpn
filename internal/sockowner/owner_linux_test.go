//go:build linux && !android

package sockowner

import (
	"net"
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"testing"
	"time"
)

// A client that sends its request and closes before the lookup: its end has no owner any
// more, and nobody reads the answer.
func TestClosedClientCountsAsAnotherAccount(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	client, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	client.Write([]byte("POST /api/password HTTP/1.1\r\n\r\n"))
	client.Close()
	server, err := ln.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	time.Sleep(100 * time.Millisecond)
	if other, err := OtherAccount(server); !other || err != nil {
		t.Fatal(other, err)
	}
}

// A real other account (nobody): only root can start one, so most runs skip this.
func TestAnotherAccountOnLinux(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skip("only root can connect as another account")
	}
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("no python3")
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := strconv.Itoa(ln.Addr().(*net.TCPAddr).Port)
	cmd := exec.Command(python, "-c", "import socket, sys, time; s = socket.create_connection(('127.0.0.1', int(sys.argv[1]))); time.sleep(30)", port)
	cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: 65534, Gid: 65534}}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { cmd.Process.Kill(); cmd.Wait() }()
	server, err := ln.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	if other, err := OtherAccount(server); !other || err != nil {
		t.Fatal(other, err)
	}
}
