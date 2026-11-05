//go:build !darwin && !linux && !windows

package sysinfo

// Other systems: priority and signals work (unix.go); nothing is measured.

const PerProcess = false

func Procs() ([]Proc, error)         { return nil, ErrUnsupported }
func ProcUsage(int) (Usage, bool)    { return Usage{}, false }
func MachineStats() (Machine, error) { return Machine{}, ErrUnsupported }
func Disks() ([]Disk, error)         { return nil, ErrUnsupported }
func currentNice(prio int) int       { return prio }
func lowerIO(int) error              { return nil }
