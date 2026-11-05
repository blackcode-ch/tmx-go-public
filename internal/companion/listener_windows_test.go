package companion

import (
	"encoding/binary"
	"golang.org/x/sys/windows"
	"net"
	"testing"
)

func TestListenerSocketOwnerTuple(t *testing.T) {
	for _, family := range []uintptr{windows.AF_INET, windows.AF_INET6} {
		rowSize, localIP, remoteIP := 24, net.IPv4(127, 0, 0, 1).To4(), net.IPv4(127, 0, 0, 1).To4()
		lp, rp, sp, pp := 8, 16, 0, 20
		la, ra := 4, 12
		if family == windows.AF_INET6 {
			rowSize, localIP, remoteIP = 56, net.IPv6loopback, net.IPv6loopback
			lp, rp, sp, pp = 20, 44, 48, 52
			la, ra = 0, 24
		}
		server, client := &net.TCPAddr{IP: localIP, Port: 1234}, &net.TCPAddr{IP: remoteIP, Port: 4321}
		b := make([]byte, 4+rowSize)
		binary.LittleEndian.PutUint32(b, 1)
		r := b[4:]
		copy(r[la:], localIP)
		copy(r[ra:], remoteIP)
		binary.BigEndian.PutUint16(r[lp:], uint16(server.Port))
		binary.BigEndian.PutUint16(r[rp:], uint16(client.Port))
		binary.LittleEndian.PutUint32(r[sp:], 5)
		binary.LittleEndian.PutUint32(r[pp:], 77)
		if !socketOwnedBy(b, family, 77, server, client) {
			t.Fatal("matching tuple rejected")
		}
		if socketOwnedBy(b, family, 78, server, client) {
			t.Fatal("wrong PID accepted")
		}
		if socketOwnedBy(b, family, 77, client, server) {
			t.Fatal("reversed endpoints accepted")
		}
		for _, offset := range []int{la, ra, lp, rp, sp, pp} {
			changed := append([]byte(nil), b...)
			changed[4+offset] ^= 1
			if socketOwnedBy(changed, family, 77, server, client) {
				t.Fatalf("mismatched tuple accepted at %d", offset)
			}
		}
		for _, short := range [][]byte{nil, b[:3], b[:len(b)-1]} {
			if socketOwnedBy(short, family, 77, server, client) {
				t.Fatal("truncated table accepted")
			}
		}
		binary.LittleEndian.PutUint32(b, 999)
		if socketOwnedBy(b, family, 77, server, client) {
			t.Fatal("invalid row count accepted")
		}
	}
}
