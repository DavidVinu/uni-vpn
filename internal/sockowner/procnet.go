package sockowner

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"net/netip"
	"strconv"
	"strings"
)

// parseProcNet reads Linux's /proc/net/tcp or /proc/net/tcp6:
//
//	sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
//	 0: 0100007F:94CB 0100007F:832E 01 00000000:00000000 00:00000000 00000000 65534        0 257167 ...
//
// An address is the kernel's 32-bit words as hex numbers in the machine's byte order, the
// port a hex number. A closed end has inode 0 (and uid 0).
func parseProcNet(data []byte, order binary.ByteOrder) ([]row, error) {
	lines := bytes.Split(data, []byte("\n"))
	var rows []row
	for _, line := range lines[1:] {
		fields := strings.Fields(string(line))
		if len(fields) == 0 {
			continue
		}
		if len(fields) < 10 {
			return nil, fmt.Errorf("unexpected line %q", line)
		}
		local, err := procAddr(fields[1], order)
		if err != nil {
			return nil, err
		}
		remote, err := procAddr(fields[2], order)
		if err != nil {
			return nil, err
		}
		uid, err := strconv.ParseUint(fields[7], 10, 32)
		if err != nil {
			return nil, err
		}
		rows = append(rows, row{local: local, remote: remote, owner: uint32(uid), known: fields[9] != "0"})
	}
	return rows, nil
}

func procAddr(field string, order binary.ByteOrder) (netip.AddrPort, error) {
	host, port, _ := strings.Cut(field, ":")
	raw, err := hex.DecodeString(host)
	if err != nil || (len(raw) != 4 && len(raw) != 16) {
		return netip.AddrPort{}, fmt.Errorf("unexpected address %q", field)
	}
	for i := 0; i < len(raw); i += 4 {
		order.PutUint32(raw[i:], binary.BigEndian.Uint32(raw[i:]))
	}
	number, err := strconv.ParseUint(port, 16, 16)
	if err != nil {
		return netip.AddrPort{}, fmt.Errorf("unexpected address %q", field)
	}
	addr, _ := netip.AddrFromSlice(raw)
	return netip.AddrPortFrom(addr.Unmap(), uint16(number)), nil
}
