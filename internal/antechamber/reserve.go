package antechamber

import (
	"os"
	"strconv"
	"strings"

	"github.com/blackcode-ch/tmx-go/internal/sysinfo"
)

// DefaultReserve is the most a volume keeps free by the reserve rule (Python's diskspace.RESERVE).
const DefaultReserve int64 = 2 << 30

// diskSpace reports (free, total) bytes of the volume holding a path; tests replace it.
var diskSpace = sysinfo.DiskSpace

// ReserveFor is Python's diskspace.reserve_for: 2 GB, or a tenth of a smaller volume.
func ReserveFor(total uint64) int64 {
	return min(DefaultReserve, int64(total/10))
}

// ReserveAt is the number of bytes to keep free on the volume holding path. TMX_DISK_RESERVE
// (bytes) overrides the rule (the Python test suite sets it to 0).
func ReserveAt(path string) int64 {
	return reserveAt(path, os.Getenv("TMX_DISK_RESERVE"))
}

func reserveAt(path, env string) int64 {
	if env = strings.TrimSpace(env); env != "" {
		if n, err := strconv.ParseInt(env, 10, 64); err == nil {
			return max(0, n)
		}
	}
	_, total, err := diskSpace(path)
	if err != nil {
		return DefaultReserve
	}
	return ReserveFor(total)
}
