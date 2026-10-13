package sockowner

import (
	"encoding/binary"
	"net/netip"
	"testing"
)

var (
	apiServer = netip.MustParseAddrPort("127.0.0.1:38091")
	apiClient = netip.MustParseAddrPort("127.0.0.1:50000")
)

func TestFind(t *testing.T) {
	ours := row{local: apiServer, remote: apiClient, owner: 501, known: true}
	for _, c := range []struct {
		name  string
		rows  []row
		other bool
		fails bool
	}{
		{"same account", []row{ours, {apiClient, apiServer, 501, true}}, false, false},
		{"another account", []row{ours, {apiClient, apiServer, 502, true}}, true, false},
		{"closed by its program", []row{ours, {apiClient, apiServer, 0, false}}, true, false},
		{"an earlier closed one first", []row{{apiClient, apiServer, 0, false}, {apiClient, apiServer, 501, true}, ours},
			false, false},
		{"client's end missing", []row{ours}, false, true},
		{"our end missing", []row{{apiClient, apiServer, 501, true}}, false, true},
		{"our end not ours", []row{{apiServer, apiClient, 502, true}, {apiClient, apiServer, 502, true}}, false, true},
	} {
		r, err := find(c.rows, apiClient, apiServer, 501)
		if (err != nil) != c.fails || (err == nil && otherOwner(r, 501) != c.other) {
			t.Error(c.name, r, err)
		}
	}
}

// Captured on Linux (x86-64): a server on 127.0.0.1:38091 (94CB) run by root, a client of root
// (8328), one of nobody (65534, 832E), and one that sent a request and closed at once (8338,
// FIN_WAIT2 without an inode).
const procNetTCP = `  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   2: 0100007F:94CB 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 256149 1 00000000f11e922b 100 0 0 10 0
  71: 0100007F:94CB 0100007F:8328 01 00000000:00000000 00:00000000 00000000     0        0 256151 1 00000000a0423f63 20 0 0 10 -1
 112: 0100007F:94CB 0100007F:832E 01 00000000:00000000 00:00000000 00000000     0        0 256155 1 00000000cf44eb9c 20 0 0 10 -1
 113: 0100007F:8328 0100007F:94CB 01 00000000:00000000 00:00000000 00000000     0        0 256150 2 00000000fdb419a4 20 0 0 10 -1
 114: 0100007F:832E 0100007F:94CB 01 00000000:00000000 00:00000000 00000000 65534        0 257167 2 0000000031270191 20 0 0 10 -1
 115: 0100007F:8338 0100007F:94CB 05 00000000:00000000 03:00001760 00000000     0        0 0 3 00000000b800cd3e
 180: 0100007F:94CB 0100007F:8338 08 00000000:00000013 00:00000000 00000000     0        0 256157 1 00000000570ccbb7 20 4 28 10 -1
`

// The same format for IPv6 (that machine had none): a dual-stack client of uid 1000 on port
// 8340, and a connection on ::1.
const procNetTCP6 = `  sl  local_address                         remote_address                        st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 0000000000000000FFFF00000100007F:8340 0000000000000000FFFF00000100007F:94CB 01 00000000:00000000 00:00000000 00000000  1000        0 300001 1 0000000000000000 20 0 0 10 -1
   1: 00000000000000000000000001000000:1F90 00000000000000000000000001000000:D431 01 00000000:00000000 00:00000000 00000000  1000        0 300002 1 0000000000000000 20 0 0 10 -1
`

func TestParseProcNet(t *testing.T) {
	rows, err := parseProcNet([]byte(procNetTCP), binary.LittleEndian)
	if err != nil || len(rows) != 7 {
		t.Fatal(rows, err)
	}
	for _, c := range []struct {
		client string
		other  bool
	}{{"127.0.0.1:33576", false}, {"127.0.0.1:33582", true}, {"127.0.0.1:33592", true}} {
		r, err := find(rows, netip.MustParseAddrPort(c.client), apiServer, 0)
		if err != nil || otherOwner(r, 0) != c.other {
			t.Error(c.client, r, err)
		}
	}
	six, err := parseProcNet([]byte(procNetTCP6), binary.LittleEndian)
	want := []row{
		{netip.MustParseAddrPort("127.0.0.1:33600"), apiServer, 1000, true},
		{netip.MustParseAddrPort("[::1]:8080"), netip.MustParseAddrPort("[::1]:54321"), 1000, true},
	}
	if err != nil || len(six) != 2 || six[0] != want[0] || six[1] != want[1] {
		t.Fatal(six, err)
	}
	for _, bad := range []string{"header\n 0: 0100007F:94CB\n", "header\n 0: 0100007G:94CB 0100007F:8328 01 0 0 0 0 0 1\n",
		"header\n 0: 0100007F:94CB 0100007F:8328 01 0 0 0 x 0 1\n"} {
		if _, err := parseProcNet([]byte(bad), binary.LittleEndian); err == nil {
			t.Error(bad)
		}
	}
}

// pcbList builds what net.inet.tcp.pcblist_n returns, from XNU's struct layout: there is no
// macOS to capture it from here. TestOwnConnection reads the real one on macOS.
type pcb struct {
	local, remote netip.AddrPort
	vflag         byte
	uid           uint32
	socket        bool // false: in TIME_WAIT, without a socket
}

func pcbList(pcbs ...pcb) []byte {
	le := binary.LittleEndian
	record := func(size int, kind uint32) []byte {
		rec := make([]byte, (size+7)&^7)
		le.PutUint32(rec, uint32(size))
		le.PutUint32(rec[4:], kind)
		return rec
	}
	gen := make([]byte, 24) // struct xinpgen
	le.PutUint32(gen, 24)
	le.PutUint32(gen[4:], uint32(len(pcbs)))
	buf := append([]byte{}, gen...)
	for i, p := range pcbs {
		handle := uint64(0xfeed0000 + i)
		in := record(104, xsoInpcb)
		le.PutUint64(in[inpcbHandle:], handle)
		binary.BigEndian.PutUint16(in[inpcbFport:], p.remote.Port())
		binary.BigEndian.PutUint16(in[inpcbLport:], p.local.Port())
		in[inpcbVflag] = p.vflag
		for at, addr := range map[int]netip.Addr{inpcbFaddr: p.remote.Addr(), inpcbLaddr: p.local.Addr()} {
			if p.vflag&inpIPv4 != 0 {
				a := addr.As4()
				copy(in[at+12:], a[:])
			} else {
				a := addr.As16()
				copy(in[at:], a[:])
			}
		}
		so := record(104, xsoSocket)
		if p.socket {
			le.PutUint64(so[socketPCB:], handle)
			le.PutUint32(so[socketUID:], p.uid)
		}
		buf = append(buf, in...)
		buf = append(buf, so...)
		buf = append(buf, record(32, 0x002)...)  // xsockbuf_n, receiving
		buf = append(buf, record(32, 0x004)...)  // and sending
		buf = append(buf, record(136, 0x008)...) // xsockstat_n
		buf = append(buf, record(236, 0x020)...) // xtcpcb_n
	}
	return append(buf, gen...)
}

func TestParsePCBList(t *testing.T) {
	mapped := func(a netip.AddrPort) netip.AddrPort {
		return netip.AddrPortFrom(netip.AddrFrom16(a.Addr().As16()), a.Port())
	}
	other := netip.MustParseAddrPort("127.0.0.1:50001")
	gone := netip.MustParseAddrPort("127.0.0.1:50002")
	dual := netip.MustParseAddrPort("127.0.0.1:50003")
	list := pcbList(
		pcb{apiServer, apiClient, inpIPv4, 501, true},
		pcb{apiClient, apiServer, inpIPv4, 501, true},
		pcb{apiServer, other, inpIPv4, 501, true},
		pcb{other, apiServer, inpIPv4, 502, true},
		pcb{gone, apiServer, inpIPv4, 0, false},
		pcb{apiServer, gone, inpIPv4, 501, true},
		pcb{mapped(dual), mapped(apiServer), inpIPv6, 502, true},
		pcb{apiServer, dual, inpIPv4, 501, true},
	)
	rows := parsePCBList(list)
	if len(rows) != 8 {
		t.Fatal(rows)
	}
	for _, c := range []struct {
		client netip.AddrPort
		other  bool
	}{{apiClient, false}, {other, true}, {gone, true}, {dual, true}} {
		r, err := find(rows, c.client, apiServer, 501)
		if err != nil || otherOwner(r, 501) != c.other {
			t.Error(c.client, r, err)
		}
	}
	// A record it cannot read ends the list; what came before stays.
	cut := append(pcbList(pcb{apiServer, apiClient, inpIPv4, 501, true})[:24+2*104], 3, 0, 0, 0, 0, 0, 0, 0)
	if rows := parsePCBList(append(cut, make([]byte, 24)...)); len(rows) != 1 || !rows[0].known {
		t.Fatal(rows)
	}
	// A socket record that belongs to no connection before it names no owner.
	swapped := pcbList(pcb{apiServer, apiClient, inpIPv4, 501, true})
	binary.LittleEndian.PutUint64(swapped[24+104+socketPCB:], 7)
	if rows := parsePCBList(swapped); len(rows) != 1 || rows[0].known {
		t.Fatal(rows)
	}
	for _, short := range [][]byte{nil, {24, 0, 0}, {200, 0, 0, 0, 0, 0, 0, 0}} {
		if rows := parsePCBList(short); rows != nil {
			t.Error(short, rows)
		}
	}
}

// tcpTableBytes builds GetExtendedTcpTable's answer from its documented layout (a Windows
// test reads the real one).
func tcpTableBytes(six bool, rows ...row) []byte {
	le := binary.LittleEndian
	buf := le.AppendUint32(nil, uint32(len(rows)))
	for _, r := range rows {
		if six {
			local, remote := r.local.Addr().As16(), r.remote.Addr().As16()
			buf = append(buf, local[:]...)
			buf = le.AppendUint32(buf, 0)
			buf = binary.BigEndian.AppendUint16(buf, r.local.Port())
			buf = append(buf, 0, 0)
			buf = append(buf, remote[:]...)
			buf = le.AppendUint32(buf, 0)
			buf = binary.BigEndian.AppendUint16(buf, r.remote.Port())
			buf = append(buf, 0, 0)
			buf = le.AppendUint32(buf, 5) // ESTABLISHED
		} else {
			local, remote := r.local.Addr().As4(), r.remote.Addr().As4()
			buf = le.AppendUint32(buf, 5)
			buf = append(buf, local[:]...)
			buf = binary.BigEndian.AppendUint16(buf, r.local.Port())
			buf = append(buf, 0, 0)
			buf = append(buf, remote[:]...)
			buf = binary.BigEndian.AppendUint16(buf, r.remote.Port())
			buf = append(buf, 0, 0)
		}
		buf = le.AppendUint32(buf, r.owner)
	}
	return buf
}

func TestParseTCPTable(t *testing.T) {
	want := []row{
		{apiServer, apiClient, 4321, true},
		{apiClient, apiServer, 8765, true},
		{netip.MustParseAddrPort("127.0.0.1:50002"), apiServer, 0, false}, // TIME_WAIT
	}
	rows, err := parseTCPTable(tcpTableBytes(false, want...))
	if err != nil || len(rows) != 3 || rows[0] != want[0] || rows[1] != want[1] || rows[2] != want[2] {
		t.Fatal(rows, err)
	}
	dual := row{netip.MustParseAddrPort("[::ffff:127.0.0.1]:50003"), netip.MustParseAddrPort("[::ffff:127.0.0.1]:38091"),
		8765, true}
	rows, err = parseTCP6Table(tcpTableBytes(true, dual))
	if err != nil || len(rows) != 1 || rows[0] != (row{unmap(dual.local), apiServer, 8765, true}) {
		t.Fatal(rows, err)
	}
	full := tcpTableBytes(false, want...)
	for _, short := range [][]byte{nil, full[:len(full)-1]} {
		if _, err := parseTCPTable(short); err == nil {
			t.Error(short)
		}
	}
}

func TestOtherSession(t *testing.T) {
	// Windows: uni-vpn is process 100 in session 1.
	sessions := map[uint32]uint32{100: 1, 200: 1, 300: 2, 400: 0}
	for _, c := range []struct {
		name  string
		self  uint32
		owner uint32
		other bool
		fails bool
	}{
		{"same session", 100, 200, false, false},
		{"another account's session", 100, 300, true, false},
		// An antivirus that passes the app's or browser's traffic through itself.
		{"a service", 100, 400, false, true},
		{"the client's program ended", 100, 500, false, true},
		{"uni-vpn outside a session", 400, 300, false, true},
	} {
		other, err := otherSession(sessions, c.self, c.owner)
		if other != c.other || (err != nil) != c.fails {
			t.Error(c.name, other, err)
		}
	}
}
