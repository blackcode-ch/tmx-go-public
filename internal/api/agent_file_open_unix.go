//go:build !windows

package api

import (
	"os"
	"syscall"
)

// A FIFO created by an agent must not leave an HTTP reader blocked in open.
func openAgentFile(root *os.Root, name string) (*os.File, error) {
	return root.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK, 0)
}
