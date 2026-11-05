package cli

import (
	"log/slog"
	"os/user"
	"strings"

	"github.com/blackcode-ch/tmx-go/internal/config"
	"github.com/blackcode-ch/tmx-go/internal/p2p"
)

// startP2P creates the daemon's peer-to-peer node from config.json "p2p" (zero config:
// QUIC+TCP on a remembered port, public DHT in client mode for relays, router port mapping,
// mDNS while there are paired peers). It returns nil when p2p is off or cannot start: the
// rest of tmx works without it.
func startP2P(paths config.Paths, cfg config.Config, log *slog.Logger) *p2p.Host {
	pc := cfg.P2P
	if !pc.On() {
		log.Info("p2p off (config.json p2p.enabled = false)")
		return nil
	}
	key, err := p2p.LoadOrCreateKey(paths.P2PKeyFile())
	if err != nil {
		log.Error("p2p identity", "err", err)
		return nil
	}
	o := p2p.Options{
		Key: key, StateFile: paths.P2PStateFile(), Name: defaultDisplayName(cfg), Version: config.Version,
		DHT: cfg.EffectiveDHT(), MDNS: pc.MDNSOn(), NATPortMap: pc.NATPortMapOn(), Logger: log,
	}
	if pc != nil {
		o.Port, o.Relays, o.Bootstrap, o.AllowLoopback, o.ListenAddrs = pc.Port, pc.Relays, pc.Bootstrap, pc.Loopback, pc.ListenAddrs
	}
	h, err := p2p.New(o)
	if err != nil {
		log.Error("p2p start", "err", err)
		return nil
	}
	if pc != nil && strings.TrimSpace(pc.Name) != "" {
		h.SetDisplayName(pc.Name) // config wins over a name set earlier
	}
	return h
}

// defaultDisplayName: config p2p.name, else the OS account's full name, else its login, else the machine.
func defaultDisplayName(cfg config.Config) string {
	if cfg.P2P != nil && strings.TrimSpace(cfg.P2P.Name) != "" {
		return strings.TrimSpace(cfg.P2P.Name)
	}
	if u, err := user.Current(); err == nil {
		if n := strings.TrimSpace(u.Name); n != "" {
			return n
		}
		if n := strings.TrimSpace(u.Username); n != "" {
			if i := strings.LastIndexAny(n, `\/`); i >= 0 {
				n = n[i+1:]
			}
			return n
		}
	}
	return cfg.Machine
}
