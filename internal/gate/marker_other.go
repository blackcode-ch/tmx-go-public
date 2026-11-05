//go:build !windows

package gate

import "github.com/blackcode-ch/tmx-go/internal/config"

func platformMarker(p config.Paths) Marker { return FileMarker(p.Home + ".gate-enrolled") }
