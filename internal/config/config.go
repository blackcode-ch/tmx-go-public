package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/blackcode-ch/tmx-go/internal/guard"
	"github.com/blackcode-ch/tmx-go/internal/model"
)

// DefaultPort isolates the trial from existing daemon installations.
const DefaultPort = 8797

// Profiles. "trial" is the side-by-side trial: local-only, every external service off.
// "production" is a machine whose primary tmx is this daemon (DESIGN.md "Production profile"):
// it may reach the Python fleet's daemons (Peers) and honours the owner's Companion and WhatsApp
// switches, but peer-to-peer (libp2p, DHT, mDNS, NAT port mapping) and automatic updates stay
// off in v1 whatever config.json says: the fleet's transport is the pair-token API, and there is
// no signed release path yet (P1.22).
const (
	ProfileTrial      = "trial"
	ProfileProduction = "production"
)

// Production reports whether this is the production profile.
func (c Config) Production() bool { return c.Profile == ProfileProduction }

// EnvPort overrides config.json for both the daemon and its clients.
const EnvPort = "TMXG_PORT"

// Config is config.json. Zero fields take their defaults on Load.
type Config struct {
	Owner              *OwnerConfig    `json:"owner,omitempty"`
	SecretFolders      []string        `json:"secret_folders,omitempty"`
	TrustedBackupRoots []string        `json:"trusted_backup_roots,omitempty"`
	Transfers          *TransferConfig `json:"transfers,omitempty"`
	Tasks              *TaskConfig     `json:"tasks,omitempty"`
	Supervisor         *string         `json:"supervisor,omitempty"`  // nil = architect; explicit empty = unique-head fallback
	AutoShelve         *bool           `json:"auto_shelve,omitempty"` // nil = on; workers idle two hours or explicitly done
	Warnings           []string        `json:"-"`                     // runtime notices; never persisted or used to enable services
	API                APIConfig       `json:"api,omitempty"`
	Profile            string          `json:"profile,omitempty"`
	Machine            string          `json:"machine"`
	Workspace          string          `json:"workspace"`
	Port               int             `json:"port"`
	ClaudeCommand      string          `json:"claude_command,omitempty"` // "" = claude on PATH
	// SignInCommands are the executables the AI accounts' sign-in, sign-out and checks run for
	// codex and agy ("codex", "antigravity"), when the one on PATH must not be used (Hostb's
	// production start puts exit-78 stubs first so no Codex/agy agent starts); agents still
	// start whatever is on PATH. "" or missing = PATH.
	SignInCommands map[string]string `json:"sign_in_commands,omitempty"`
	HeadHarness    string            `json:"head_harness,omitempty"` // "" = claude, and an existing head keeps its harness; else claude or codex
	// Policy seeds the built-in agent profiles when tmx.db has none yet (first start, or the
	// upgrade to profiles); nil = model.DefaultPolicy(). Afterwards edit the profiles instead.
	Policy    *model.Policy    `json:"policy,omitempty"`
	Guard     *guard.Settings  `json:"guard,omitempty"`     // nil = on for every agent
	Resources *Resources       `json:"resources,omitempty"` // nil = DefaultResources()
	P2P       *P2PConfig       `json:"p2p,omitempty"`       // nil = peer-to-peer off
	Companion *CompanionConfig `json:"companion,omitempty"` // nil = Companion off
	Update    *UpdateConfig    `json:"update,omitempty"`    // nil = automatic updates off
	Voice     *VoiceConfig     `json:"voice,omitempty"`     // nil = the defaults (voice.go)
	Recorder  *RecorderConfig  `json:"recorder,omitempty"`  // nil = the recorder's defaults
	Summaries *SummaryConfig   `json:"summaries,omitempty"` // nil = the defaults (summary.go)
	WhatsApp  *WhatsAppConfig  `json:"whatsapp,omitempty"`  // nil = off (whatsapp.go)
	Hosts     []Host           `json:"hosts,omitempty"`     // other local daemons merged into Overview (hosts.go); nil = none
	Peers     []Peer           `json:"peers,omitempty"`
	// FleetAuthorities: "name@host" senders whose peer mail counts as the owner's standing order
	// (internal/fleet/authority.go); nil = the default (architect@hosta, tmx-lead@hosta).
	FleetAuthorities []string `json:"fleet_authorities,omitempty"`
	// AgentBypass: Claude agents guarded by tmx's hook run in bypass mode, so the guard alone
	// decides what asks the owner (internal/fleet/guard.go); nil = on, false = Claude's prompts.
	AgentBypass *bool `json:"agent_bypass,omitempty"` // the Python fleet's other machines (peers.go); production only
}

// OwnerConfig supplies names used by the manipulation detector. Empty names retain generic role matching.
type OwnerConfig struct {
	DisplayName string   `json:"display_name,omitempty"`
	Aliases     []string `json:"aliases,omitempty"`
}

func (o *OwnerConfig) Names() []string {
	if o == nil {
		return nil
	}
	return append([]string{o.DisplayName}, o.Aliases...)
}

type TransferConfig struct {
	HoldNeedsHuman bool `json:"hold_needs_human,omitempty"`
}

// RecorderConfig is config.json "recorder" (DESIGN.md "Recorder"); zero fields take the defaults.
type RecorderConfig struct {
	VideoKbps      int `json:"video_kbps,omitempty"`      // screen video bitrate; 0 = 1500
	FPS            int `json:"fps,omitempty"`             // screen frames per second; 0 = 15
	MaxMinutes     int `json:"max_minutes,omitempty"`     // longest recording; 0 = no limit (the owner, 2 Nov: "no time limit"; only his Stop ends it)
	SegmentMB      int `json:"segment_mb,omitempty"`      // a new segment after this many MB; 0 = 20 (chat files take 25)
	SegmentMinutes int `json:"segment_minutes,omitempty"` // … or after this many minutes; 0 = 5 (the owner: "chunks of 5 min")
}

// Resolved returns the recorder settings with the defaults filled in (r may be nil).
func (r *RecorderConfig) Resolved() RecorderConfig {
	out := RecorderConfig{}
	if r != nil {
		out = *r
	}
	def := func(v *int, d, max int) {
		if *v <= 0 {
			*v = d
		}
		if *v > max {
			*v = max
		}
	}
	def(&out.VideoKbps, 1500, 20000)
	def(&out.FPS, 15, 60)
	if out.MaxMinutes < 0 {
		out.MaxMinutes = 0 // no limit
	}
	def(&out.SegmentMB, 20, 24) // a segment must stay a chat file (25 MB)
	def(&out.SegmentMinutes, 5, 60)
	return out
}

// UpdateConfig is config.json "update" (DESIGN.md "Updates").
type UpdateConfig struct {
	// Auto installs new releases by itself (once no agent is working); nil = false.
	Auto *bool `json:"auto,omitempty"`
	// Channel: "stable" (fleet releases) or "latest" (also fleet prereleases).
	Channel string `json:"channel,omitempty"`
	// CheckHours between checks; 0 = 6.
	CheckHours int `json:"check_hours,omitempty"`
	// Repo the releases come from (owner/name); "" = DefaultUpdateRepo.
	// PublicUpdateRepo serves fleet releases without requiring a GitHub token.
	Repo string `json:"repo,omitempty"`
	// FleetPublicKey is the base64 Ed25519 fleet key (the pre-fleet_keys form, read as one
	// ed25519 entry with id "fleet-release-v1"). Empty and no FleetKeys disables updates.
	FleetPublicKey string `json:"fleet_public_key,omitempty"`
	// FleetKeys is the pinned release key set (1-4 entries; DESIGN.md "Updates"). See fleetkeys.go.
	FleetKeys []FleetKey `json:"fleet_keys,omitempty"`
	// Require lists the signature types a release must carry, each by a pinned key of that type;
	// empty = ["ed25519"].
	Require []string `json:"require,omitempty"`
	// KeysSerial is the serial of the last signed KEYS rotation statement applied (0 = none); a
	// statement must carry a higher one (DESIGN.md "Updates", rotation).
	KeysSerial uint64 `json:"keys_serial,omitempty"`

	// fleet_keys/require as written when they had the wrong JSON shape (see UnmarshalJSON): kept so
	// a save writes them back unchanged, and reported by PinnedKeys.
	rawKeys, rawRequire json.RawMessage
	shapeErr            error
}

// Update channels.
const (
	ChannelStable     = "stable"
	ChannelLatest     = "latest"
	DefaultUpdateRepo = "blackcode-ch/tmx-go"
	PublicUpdateRepo  = "blackcode-ch/tmx-go-public"
)

// Resolved returns the update settings with the defaults filled in (u may be nil).
func (u *UpdateConfig) Resolved() UpdateConfig {
	out := UpdateConfig{}
	if u != nil {
		out = *u
	}
	if out.Auto == nil {
		on := false
		out.Auto = &on
	}
	if out.Channel != ChannelLatest {
		out.Channel = ChannelStable
	}
	if out.CheckHours <= 0 {
		out.CheckHours = 6
	}
	if out.Repo == "" {
		out.Repo = DefaultUpdateRepo
	}
	return out
}

// CompanionConfig is config.json "companion" (DESIGN.md "Companion"). The agent key Companion
// gave tmx is not here: it lives in secrets/companion-agent-token (`tmx companion connect`).
type CompanionConfig struct {
	Enabled *bool `json:"enabled,omitempty"`
	// URL of Companion's local agent server, e.g. http://127.0.0.1:8010 (loopback only);
	// "" = discovered (Companion's engine.json, then its engine state, then port 8010).
	URL string `json:"url,omitempty"`
}

// P2PConfig is config.json "p2p" (DESIGN.md "Peer-to-peer network"). Everything is optional:
// the defaults need no configuration at all.
type P2PConfig struct {
	FileMaxBytes int64    `json:"file_max_bytes,omitempty"` // 0 = 25 MiB; may lower the protocol ceiling
	Enabled      *bool    `json:"enabled,omitempty"`        // false = no p2p host at all
	Name         string   `json:"name,omitempty"`           // display name for new invites; "" = the OS account's name
	Port         int      `json:"port,omitempty"`           // fixed QUIC/TCP port; 0 = picked once, then remembered
	Relays       []string `json:"relays,omitempty"`         // extra circuit relays (/ip4/…/p2p/<id>), e.g. your own
	Bootstrap    []string `json:"bootstrap,omitempty"`      // extra DHT bootstrap peers (added to the IPFS defaults)
	DHT          *bool    `json:"dht,omitempty"`            // false = no public DHT (no relay discovery, LAN/direct only)
	// DHTExplicit: the owner set DHT themselves (PUT /api/p2p/config, a real UI action) - as
	// opposed to a default fillDefaults or the same route wrote on its own. Older config.json
	// files have an explicit "dht":false from before production honoured this field at all
	// (cfc7be0/PR #176); without this flag that stale value would be indistinguishable from a
	// genuine owner choice and PUT /api/p2p/config's enable-defaults-DHT-on logic could never
	// fire for them.
	DHTExplicit bool  `json:"dht_explicit,omitempty"`
	MDNS        *bool `json:"mdns,omitempty"` // false = no LAN discovery (otherwise on only while there are paired peers)
	// NATPortMap false = never ask the router for a port mapping (UPnP / NAT-PMP).
	NATPortMap *bool `json:"nat_port_map,omitempty"`
	// Loopback lets invites carry 127.0.0.1 addresses (two daemons on one machine, tests).
	Loopback bool `json:"loopback,omitempty"`
	// ListenAddrs replaces the default QUIC+TCP listeners (127.0.0.1/::1 on the remembered
	// port). "" = the default: never bind 0.0.0.0/:: without being asked - a wildcard bind
	// triggers a Windows Firewall prompt, so the daemon never does that on its own.
	ListenAddrs []string `json:"listen_addrs,omitempty"`
	// DebugDrain makes the daemon itself consume pairings and envelopes into GET
	// /api/p2p/net/log. Only for testing the network layer without the chat layer.
	DebugDrain bool `json:"debug_drain,omitempty"`
}

// On reports whether p2p is enabled (default false).
func (p *P2PConfig) On() bool { return p != nil && p.Enabled != nil && *p.Enabled }

func (p *P2PConfig) FileLimit() int64 {
	if p == nil || p.FileMaxBytes <= 0 || p.FileMaxBytes > 25<<20 {
		return 25 << 20
	}
	return p.FileMaxBytes
}

// DHTOn, MDNSOn and NATPortMapOn default to false.
func (p *P2PConfig) DHTOn() bool        { return p != nil && p.DHT != nil && *p.DHT }
func (p *P2PConfig) MDNSOn() bool       { return p != nil && p.MDNS != nil && *p.MDNS }
func (p *P2PConfig) NATPortMapOn() bool { return p != nil && p.NATPortMap != nil && *p.NATPortMap }

// EffectiveDHT is what the daemon actually starts with - not DHTOn's bare on-disk value.
// Production p2p (DESIGN.md "Peer-to-peer network") joins the public DHT (outbound only:
// AutoRelay/AutoNATv2, host.go:238 - no inbound listener, no NATPortMap, no firewall prompt)
// UNLESS the owner explicitly chose otherwise (P2PConfig.DHTExplicit, set only by PUT
// /api/p2p/config, a real owner+PIN action). Without this, a machine with p2p.enabled=true and
// an on-disk "dht":false left over from older fillDefaults code (cfc7be0/PR #176, before
// production honoured the field at all - exactly Hostb's config, 3 Nov) would never engage
// relay/hole-punching: a peer ID nobody behind NAT can reach, and no invite carries a usable
// address. Checked at daemon start (cli/p2p_node.go's startP2P), so an already-written
// config.json self-corrects on its very next restart - no agent write to config.json needed
// (Hostb's guard correctly refuses that), and no action from the owner either.
func (c Config) EffectiveDHT() bool {
	if !c.Production() || !c.P2P.On() {
		return c.P2P.DHTOn()
	}
	if c.P2P.DHTExplicit {
		return c.P2P.DHTOn()
	}
	return true
}

// EffectivePolicy returns the override from config.json, else the default policy.
func (c Config) EffectivePolicy() model.Policy {
	if c.Policy != nil && len(c.Policy.Levels) > 0 {
		return *c.Policy
	}
	return model.DefaultPolicy()
}

// URL is the daemon's base URL.
func (c Config) URL() string { return fmt.Sprintf("http://127.0.0.1:%d", c.Port) }

// Load reads config.json (a missing file is fine) and fills defaults. When the file is
// missing it is written with the defaults, so the owner has something to edit.
func Load(p Paths) (Config, error) {
	var c Config
	missing := false
	b, err := os.ReadFile(p.ConfigFile())
	switch {
	case err == nil:
		if err := json.Unmarshal(b, &c); err != nil {
			return c, fmt.Errorf("%s: %w", p.ConfigFile(), err)
		}
	case errors.Is(err, os.ErrNotExist):
		missing = true
	default:
		return c, err
	}
	if c.Profile != "" && c.Profile != ProfileTrial && c.Profile != ProfileProduction {
		return c, fmt.Errorf("unsupported config profile %q (want %q or %q)", c.Profile, ProfileTrial, ProfileProduction)
	}
	c.Warnings = c.ProfileWarnings()
	if !c.Production() && len(c.Warnings) == 0 {
		if _, err := os.Stat(p.WhatsAppDB()); err == nil {
			c.Warnings = []string{TrialServicesWarning}
		}
	}
	c.fillDefaults()
	if err := c.API.Validate(); err != nil {
		return c, err
	}
	if err := c.validatePeers(); err != nil {
		return c, err
	}
	stored := c
	if raw := os.Getenv(EnvPort); raw != "" {
		port, err := strconv.Atoi(raw)
		if err != nil {
			return c, fmt.Errorf("%s must be an integer port", EnvPort)
		}
		c.Port = port
	}
	if c.Port < 1 || c.Port > 65535 {
		return c, fmt.Errorf("port must be between 1 and 65535")
	}
	if !ValidListenPort(c.Port) {
		return c, fmt.Errorf("port %d is reserved for existing installations (8777 Python tmx, 8787 tmx-go v0.1, 43911, 4001); choose another port", c.Port)
	}
	if missing {
		return c, Save(p, stored)
	}
	return c, nil
}

// Save writes config.json atomically.
func Save(p Paths, c Config) error {
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return WriteFileAtomic(p.ConfigFile(), append(b, '\n'), 0o600)
}

func (c *Config) fillDefaults() {
	if c.Profile == "" {
		c.Profile = ProfileTrial
	}
	// Automatic updates stay off everywhere (no signed release path yet); the trial also stays
	// local-only (no WhatsApp, no Companion, no Peers, no peer-to-peer), including when loading
	// an older config. In production, config.json decides peer-to-peer (the owner, 3 Nov 15:47:
	// the pair-token transport doesn't cover person-to-person contacts/chats, which is what
	// peer-to-peer is for; the "no signed release" reasoning was about auto-update, not this).
	off := func() *bool { v := false; return &v }
	if c.P2P == nil {
		c.P2P = &P2PConfig{}
	}
	if c.WhatsApp == nil {
		c.WhatsApp = &WhatsAppConfig{}
	}
	if c.Companion == nil {
		c.Companion = &CompanionConfig{}
	}
	if !c.Production() {
		c.P2P.Enabled, c.P2P.DHT, c.P2P.MDNS, c.P2P.NATPortMap = off(), off(), off(), off()
		c.WhatsApp.Enabled = off()
		c.Companion.Enabled = off()
	}
	if c.Update == nil {
		c.Update = &UpdateConfig{}
	}
	c.Update.Auto = off()

	if c.Machine == "" {
		c.Machine = shortHostname()
	}
	if c.Workspace == "" {
		if home, err := os.UserHomeDir(); err == nil {
			c.Workspace = filepath.Join(home, "Projects")
		}
	}
	c.Workspace = expandHome(c.Workspace)
	if c.Port == 0 {
		c.Port = DefaultPort
	}
}

func shortHostname() string {
	h, err := os.Hostname()
	if err != nil || h == "" {
		return "machine"
	}
	if i := strings.IndexByte(h, '.'); i > 0 {
		h = h[:i]
	}
	return strings.ToLower(h)
}

func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`) {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[1:])
		}
	}
	return p
}

// WriteFileAtomic writes to a temp file in the same directory and renames it into place.
func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Chmod(perm); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
