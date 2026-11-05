package config

import (
	"encoding/json"
	"errors"
	"os"
)

// DaemonInfo is daemon.json, written by a running daemon and removed when it exits.
type DaemonInfo struct {
	PID       int    `json:"pid"`
	Port      int    `json:"port"`
	StartedAt int64  `json:"started_at"`
	Version   string `json:"version"`
}

func WriteDaemonInfo(p Paths, d DaemonInfo) error {
	b, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return err
	}
	return WriteFileAtomic(p.DaemonFile(), append(b, '\n'), 0o600)
}

// ReadDaemonInfo returns os.ErrNotExist (wrapped) when no daemon wrote the file.
func ReadDaemonInfo(p Paths) (DaemonInfo, error) {
	var d DaemonInfo
	b, err := os.ReadFile(p.DaemonFile())
	if err != nil {
		return d, err
	}
	return d, json.Unmarshal(b, &d)
}

func RemoveDaemonInfo(p Paths) error {
	err := os.Remove(p.DaemonFile())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
