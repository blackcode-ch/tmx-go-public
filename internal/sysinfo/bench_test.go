package sysinfo

import (
	"os"
	"testing"
)

func BenchmarkProcs(b *testing.B) {
	for b.Loop() {
		if _, err := Procs(); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkProcUsage(b *testing.B) {
	pid := os.Getpid()
	for b.Loop() {
		ProcUsage(pid)
	}
}

func BenchmarkMachineStats(b *testing.B) {
	for b.Loop() {
		_, _ = MachineStats()
	}
}

func BenchmarkDisks(b *testing.B) {
	for b.Loop() {
		_, _ = Disks()
	}
}

func BenchmarkDiskSpace(b *testing.B) {
	for b.Loop() {
		_, _, _ = DiskSpace(".")
	}
}
