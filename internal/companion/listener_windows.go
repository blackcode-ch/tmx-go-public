package companion

import (
	"encoding/binary"
	"net"
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

var tcpTable = windows.NewLazySystemDLL("iphlpapi.dll").NewProc("GetExtendedTcpTable")

func verifyCompanionConnection(c net.Conn) error {
	server, ok := c.RemoteAddr().(*net.TCPAddr)
	if !ok {
		return ErrListenerIdentity
	}
	client, ok := c.LocalAddr().(*net.TCPAddr)
	if !ok {
		return ErrListenerIdentity
	}
	pid := advertisedPID(server.Port)
	if pid <= 0 {
		return ErrListenerIdentity
	}
	// The four-tuple is for the established server-side socket. Checking only a
	// LISTEN row would race a process replacing the listener between probe/dial.
	family := uintptr(windows.AF_INET)
	if server.IP.To4() == nil {
		family = windows.AF_INET6
	}
	var size uint32
	rc, _, _ := tcpTable.Call(0, uintptr(unsafe.Pointer(&size)), 0, family, 4, 0)
	if rc != uintptr(windows.ERROR_INSUFFICIENT_BUFFER) || size < 4 || size > 16<<20 {
		return ErrListenerIdentity
	}
	b := make([]byte, size)
	rc, _, _ = tcpTable.Call(uintptr(unsafe.Pointer(&b[0])), uintptr(unsafe.Pointer(&size)), 0, family, 4, 0)
	if rc != 0 {
		return ErrListenerIdentity
	}
	if !socketOwnedBy(b, family, pid, server, client) {
		return ErrListenerIdentity
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return ErrListenerIdentity
	}
	defer windows.CloseHandle(h)
	name := make([]uint16, 32768)
	n := uint32(len(name))
	if windows.QueryFullProcessImageName(h, 0, &name[0], &n) != nil {
		return ErrListenerIdentity
	}
	exe, err := filepath.EvalSymlinks(windows.UTF16ToString(name[:n]))
	if err != nil {
		return ErrListenerIdentity
	}
	for _, root := range []string{filepath.Join(os.Getenv("LOCALAPPDATA"), "Programs", "Companion"), filepath.Join(os.Getenv("ProgramFiles"), "Companion")} {
		resolved, e := filepath.EvalSymlinks(root)
		if e != nil {
			continue
		}
		rel, e := filepath.Rel(strings.ToLower(resolved), strings.ToLower(exe))
		if e == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel) {
			return nil
		}
	}
	return ErrListenerIdentity
}

// socketOwnedBy parses a synthetic or OS TCP owner table. Only the exact
// established reverse tuple can authorize this connected socket.
func socketOwnedBy(b []byte, family uintptr, pid int, server, client *net.TCPAddr) bool {
	if len(b) < 4 || pid <= 0 {
		return false
	}
	rowSize := 24
	if family == windows.AF_INET6 {
		rowSize = 56
	} else if family != windows.AF_INET {
		return false
	}
	count := int(binary.LittleEndian.Uint32(b))
	if count > (len(b)-4)/rowSize {
		return false
	}
	for i := 0; i < count; i++ {
		r := b[4+i*rowSize : 4+(i+1)*rowSize]
		var local, remote net.IP
		var lp, rp, state, owner uint32
		if family == windows.AF_INET {
			state = binary.LittleEndian.Uint32(r)
			local = net.IP(r[4:8])
			lp = uint32(binary.BigEndian.Uint16(r[8:10]))
			remote = net.IP(r[12:16])
			rp = uint32(binary.BigEndian.Uint16(r[16:18]))
			owner = binary.LittleEndian.Uint32(r[20:24])
		} else {
			local = net.IP(r[:16])
			lp = uint32(binary.BigEndian.Uint16(r[20:22]))
			remote = net.IP(r[24:40])
			rp = uint32(binary.BigEndian.Uint16(r[44:46]))
			state = binary.LittleEndian.Uint32(r[48:52])
			owner = binary.LittleEndian.Uint32(r[52:56])
		}
		if state == 5 && int(owner) == pid && int(lp) == server.Port && int(rp) == client.Port && local.Equal(server.IP) && remote.Equal(client.IP) {
			return true
		}
	}
	return false
}
