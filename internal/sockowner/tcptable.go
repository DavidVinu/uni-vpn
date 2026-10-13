package sockowner

import (
	"encoding/binary"
	"errors"
	"net/netip"
)

// Windows' GetExtendedTcpTable with TCP_TABLE_OWNER_PID_CONNECTIONS: a DWORD count, then
// MIB_TCPROW_OWNER_PID rows (state, local address, local port, remote address, remote port,
// owning process: 6 DWORDs), or for IPv6 MIB_TCP6ROW_OWNER_PID rows (local address[16],
// scope, port, remote address[16], scope, port, state, owning process). Addresses and the
// ports in the low two bytes of their DWORD are in network byte order, the rest is
// little-endian. A connection in TIME_WAIT belongs to process 0.
const (
	tcpRowSize  = 24
	tcp6RowSize = 56
)

var errShortTable = errors.New("TCP table cut short")

func parseTCPTable(buf []byte) ([]row, error) {
	return parseRows(buf, tcpRowSize, func(r []byte) row {
		return row{
			local:  netip.AddrPortFrom(netip.AddrFrom4([4]byte(r[4:8])), port(r[8:])),
			remote: netip.AddrPortFrom(netip.AddrFrom4([4]byte(r[12:16])), port(r[16:])),
			owner:  binary.LittleEndian.Uint32(r[20:]),
		}
	})
}

func parseTCP6Table(buf []byte) ([]row, error) {
	return parseRows(buf, tcp6RowSize, func(r []byte) row {
		return row{
			local:  netip.AddrPortFrom(netip.AddrFrom16([16]byte(r[0:16])).Unmap(), port(r[20:])),
			remote: netip.AddrPortFrom(netip.AddrFrom16([16]byte(r[24:40])).Unmap(), port(r[44:])),
			owner:  binary.LittleEndian.Uint32(r[52:]),
		}
	})
}

func parseRows(buf []byte, size int, parse func([]byte) row) ([]row, error) {
	if len(buf) < 4 {
		return nil, errShortTable
	}
	count := int(binary.LittleEndian.Uint32(buf))
	if count > (len(buf)-4)/size {
		return nil, errShortTable
	}
	rows := make([]row, count)
	for i := range rows {
		rows[i] = parse(buf[4+i*size : 4+(i+1)*size])
		rows[i].known = rows[i].owner != 0
	}
	return rows, nil
}

func port(b []byte) uint16 { return binary.BigEndian.Uint16(b) }
