package config

import (
	"encoding/json"
	"errors"
	"os"
)

// InspectionOwnerNames reads the local display name shown in pairing and chat UI,
// even when P2P is disabled. Optional config names supplement the stored name.
// No remote peer names are used as owner identities. On read/parse errors, the
// configured names are still returned so callers can warn and retain defaults.
func InspectionOwnerNames(paths Paths, cfg Config) ([]string, error) {
	names := cfg.Owner.Names()
	if cfg.P2P != nil {
		names = append(names, cfg.P2P.Name)
	}
	b, err := os.ReadFile(paths.P2PStateFile())
	if errors.Is(err, os.ErrNotExist) {
		return names, nil
	}
	if err != nil {
		return names, err
	}
	var profile struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(b, &profile); err != nil {
		return names, err
	}
	return append(names, profile.Name), nil
}
