package main

import (
	"bytes"
	"debug/macho"
	"encoding/binary"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// thin writes a Mac program for one processor: its header (no load commands), then body.
func thin(t *testing.T, cpu macho.Cpu, sub uint32, body string) (string, []byte) {
	t.Helper()
	var b bytes.Buffer
	binary.Write(&b, binary.LittleEndian, macho.FileHeader{Magic: macho.Magic64, Cpu: cpu, SubCpu: sub, Type: macho.TypeExec})
	b.Write(make([]byte, 4)) // reserved, in 64-bit headers
	b.WriteString(body)
	path := filepath.Join(t.TempDir(), cpu.String())
	if err := os.WriteFile(path, b.Bytes(), 0o755); err != nil {
		t.Fatal(err)
	}
	return path, b.Bytes()
}

// The universal core of the Mac update zips: both programs unchanged (the Apple Silicon one keeps
// the signature Go's linker gave it), each where the header says, as Apple's tools read it.
func TestBothProcessorsInOneFile(t *testing.T) {
	arm, armData := thin(t, macho.CpuArm64, 0, "apple silicon")
	intel, intelData := thin(t, macho.CpuAmd64, 3, "intel")
	out := filepath.Join(t.TempDir(), "uni-vpn-core")
	if err := create(out, []string{arm, intel}); err != nil {
		t.Fatal(err)
	}
	f, err := macho.OpenFat(out)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	raw, _ := os.ReadFile(out)
	want := []struct {
		cpu  macho.Cpu
		sub  uint32
		data []byte
	}{{macho.CpuArm64, 0, armData}, {macho.CpuAmd64, 3, intelData}}
	if len(f.Arches) != len(want) {
		t.Fatal(len(f.Arches))
	}
	for i, a := range f.Arches {
		w := want[i]
		if a.Cpu != w.cpu || a.SubCpu != w.sub || a.Type != macho.TypeExec || a.Align != align || a.Offset%(1<<align) != 0 {
			t.Fatalf("%+v", a.FatArchHeader)
		}
		if !bytes.Equal(raw[a.Offset:a.Offset+a.Size], w.data) {
			t.Fatal("changed", w.cpu)
		}
	}
	// Windows has no executable bit.
	if st, err := os.Stat(out); err != nil || (runtime.GOOS != "windows" && st.Mode().Perm()&0o100 == 0) {
		t.Fatal("not executable", err)
	}
	// A Linux program by mistake: refused, nothing written.
	elf := filepath.Join(t.TempDir(), "elf")
	os.WriteFile(elf, []byte("\x7fELF"), 0o755)
	other := filepath.Join(t.TempDir(), "other")
	if err := create(other, []string{arm, elf}); err == nil {
		t.Fatal("no error")
	}
	if _, err := os.Stat(other); !os.IsNotExist(err) {
		t.Fatal("written", err)
	}
}
