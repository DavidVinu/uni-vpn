// Command lipo puts Mac programs built for one processor each into one file that runs on both,
// like Apple's "lipo -create", which the update zips built on Linux cannot use
// (packaging/build-core.sh).
// Usage: go run ./packaging/lipo OUT IN...
package main

import (
	"bytes"
	"debug/macho"
	"encoding/binary"
	"fmt"
	"os"
)

// align: every program starts at a multiple of 16 KiB, the page size of Apple Silicon, as lipo
// places it.
const align = 14

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: lipo OUT IN...")
		os.Exit(2)
	}
	if err := create(os.Args[1], os.Args[2:]); err != nil {
		fmt.Fprintln(os.Stderr, "lipo:", err)
		os.Exit(1)
	}
}

func create(out string, in []string) error {
	var header, body bytes.Buffer
	binary.Write(&header, binary.BigEndian, [2]uint32{macho.MagicFat, uint32(len(in))})
	start := uint32(8 + 20*len(in)) // the header and one macho.FatArchHeader per program
	for _, name := range in {
		f, err := macho.Open(name)
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		cpu, sub := f.Cpu, f.SubCpu
		f.Close()
		data, err := os.ReadFile(name)
		if err != nil {
			return err
		}
		offset := (start + uint32(body.Len()) + 1<<align - 1) &^ (1<<align - 1)
		body.Write(make([]byte, offset-start-uint32(body.Len())))
		body.Write(data)
		binary.Write(&header, binary.BigEndian, macho.FatArchHeader{Cpu: cpu, SubCpu: sub, Offset: offset, Size: uint32(len(data)), Align: align})
	}
	return os.WriteFile(out, append(header.Bytes(), body.Bytes()...), 0o755)
}
