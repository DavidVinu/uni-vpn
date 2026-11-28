//go:build windows

package libtunnel

import (
	"bytes"
	"os"
	"testing"
	"time"
)

// The pipe keeps packet boundaries both ways, and closing the stack's end ends its read, as
// the network stack needs (netstack.Stack.Close).
func TestPipePairKeepsPackets(t *testing.T) {
	mine, theirs, err := socketPair()
	if err != nil {
		t.Fatal(err)
	}
	// openconnect's end, overlapped as libopenconnect uses it; Go's poller does the same.
	other := os.NewFile(uintptr(theirs), "openconnect")
	defer other.Close()

	packets := [][]byte{bytes.Repeat([]byte{0x45}, 1400), {0x45, 1, 2}, bytes.Repeat([]byte{0x60}, 9000)}
	buf := make([]byte, 65535)
	for _, p := range packets {
		if _, err := other.Write(p); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range packets {
		n, err := mine.Read(buf)
		if err != nil || !bytes.Equal(buf[:n], p) {
			t.Fatalf("read %d bytes (%v), want one packet of %d", n, err, len(p))
		}
	}
	for _, p := range packets {
		if _, err := mine.Write(p); err != nil {
			t.Fatal(err)
		}
		n, err := other.Read(buf)
		if err != nil || !bytes.Equal(buf[:n], p) {
			t.Fatalf("read %d bytes (%v), want one packet of %d", n, err, len(p))
		}
	}

	done := make(chan error, 1)
	go func() {
		_, err := mine.Read(buf)
		done <- err
	}()
	time.Sleep(100 * time.Millisecond)
	mine.Close()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("read after Close returned no error")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not end the read")
	}
}
