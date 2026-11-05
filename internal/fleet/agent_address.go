package fleet

import (
	"strings"

	"github.com/blackcode-ch/tmx-go/internal/model"
)

// DecodeAgentAddress accepts Companion's room member ID and the fleet address.
// Bare names/opaque local IDs are left for the local resolver. This decodes an
// address only: it does not grant access or turn a remote agent into a local one.
func DecodeAgentAddress(value string) (name, host string, qualified bool, err error) {
	value = strings.TrimSpace(value)
	switch {
	case strings.HasPrefix(strings.ToLower(value), "tmx:"):
		parts := strings.Split(value, ":")
		if len(parts) != 3 {
			return "", "", false, errf(KindInvalid, "room agent IDs must be tmx:host:name")
		}
		host, name, qualified = parts[1], parts[2], true
	case strings.Contains(value, "@"):
		name, host, _ = strings.Cut(value, "@")
		qualified = true
	default:
		return value, "", false, nil
	}
	name, host = strings.ToLower(name), strings.ToLower(host)
	if !model.ValidName(name) || !validAgentHost(host) {
		return "", "", false, errf(KindInvalid, "invalid qualified agent address")
	}
	return name, host, qualified, nil
}

func validAgentHost(host string) bool {
	if len(host) == 0 || len(host) > 253 {
		return false
	}
	for i, c := range host {
		if c >= 'a' && c <= 'z' || c >= '0' && c <= '9' {
			continue
		}
		if i > 0 && (c == '-' || c == '.' || c == '_') {
			continue
		}
		return false
	}
	return true
}
