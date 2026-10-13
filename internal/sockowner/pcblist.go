package sockowner

import (
	"encoding/binary"
	"net/netip"
)

// macOS's sysctl net.inet.tcp.pcblist_n, what netstat reads (XNU bsd/netinet/in_pcblist.c):
// a struct xinpgen, then for each connection a struct xinpcb_n, xsocket_n, two xsockbuf_n,
// xsockstat_n and xtcpcb_n, then a closing xinpgen. Each record starts with its length and
// kind and is padded to 8 bytes. The offsets are those of XNU's structs under #pragma pack(4).
const (
	xsoSocket = 0x001
	xsoInpcb  = 0x010

	inpIPv4 = 0x1
	inpIPv6 = 0x2

	// struct xinpcb_n
	inpcbHandle  = 8  // xi_inpp
	inpcbFport   = 16 // network byte order
	inpcbLport   = 18
	inpcbVflag   = 44
	inpcbFaddr   = 48 // in6_addr, or the IPv4 address in its last 4 bytes
	inpcbLaddr   = 64
	inpcbMinSize = 80

	// struct xsocket_n
	socketPCB     = 28 // so_pcb: the xi_inpp of its connection
	socketUID     = 64
	socketMinSize = 68
)

// parsePCBList reads the connections, macOS being little-endian. It stops at the first record
// it cannot read: the connections up to there stay usable, the owner check in find still
// sees whether ours is among them.
func parsePCBList(buf []byte) []row {
	le := binary.LittleEndian
	if len(buf) < 4 {
		return nil
	}
	header := int(le.Uint32(buf))
	if header < 8 || header > len(buf) {
		return nil
	}
	end := len(buf) - header // the closing xinpgen
	var rows []row
	last := -1 // the connection whose socket record comes next
	var handle uint64
	for off := header; off+8 <= end; {
		size, kind := int(le.Uint32(buf[off:])), le.Uint32(buf[off+4:])
		if size < 8 || off+size > end {
			break
		}
		rec := buf[off : off+size]
		switch kind {
		case xsoInpcb:
			last = -1
			if size < inpcbMinSize {
				return rows
			}
			if local, remote, ok := inpcbAddrs(rec); ok {
				rows = append(rows, row{local: local, remote: remote})
				last, handle = len(rows)-1, le.Uint64(rec[inpcbHandle:])
			}
		case xsoSocket:
			if size < socketMinSize {
				return rows
			}
			// A connection in TIME_WAIT has no socket any more: an empty record.
			if last >= 0 && handle != 0 && le.Uint64(rec[socketPCB:]) == handle {
				rows[last].owner, rows[last].known = le.Uint32(rec[socketUID:]), true
			}
			last = -1
		}
		off += (size + 7) &^ 7
	}
	return rows
}

func inpcbAddrs(rec []byte) (local, remote netip.AddrPort, ok bool) {
	fport := binary.BigEndian.Uint16(rec[inpcbFport:])
	lport := binary.BigEndian.Uint16(rec[inpcbLport:])
	faddr, laddr := rec[inpcbFaddr:inpcbFaddr+16], rec[inpcbLaddr:inpcbLaddr+16]
	var f, l netip.Addr
	switch vflag := rec[inpcbVflag]; {
	case vflag&inpIPv4 != 0:
		f, l = netip.AddrFrom4([4]byte(faddr[12:])), netip.AddrFrom4([4]byte(laddr[12:]))
	case vflag&inpIPv6 != 0:
		f, l = netip.AddrFrom16([16]byte(faddr)).Unmap(), netip.AddrFrom16([16]byte(laddr)).Unmap()
	default:
		return local, remote, false
	}
	return netip.AddrPortFrom(l, lport), netip.AddrPortFrom(f, fport), true
}
