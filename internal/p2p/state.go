package p2p

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/config"
)

// state is the network layer's own persisted file (<state>/secrets/p2p-state.json, secret):
// display name, remembered listen port, open invites (with their secrets) and the paired
// peers. The chat layer keeps contacts and chats in tmx.db; this file only decides which
// peers may talk to this node and how to reach them.
type state struct {
	Name    string              `json:"name"`
	Port    int                 `json:"port"` // remembered listen port (stable addresses in invites)
	Invites []*inviteRec        `json:"invites"`
	Peers   map[string]*peerRec `json:"peers"`
	path    string
}

type inviteRec struct {
	Invite
	Secret    []byte   `json:"secret"`
	CreatedAt int64    `json:"created_at"`
	UsedBy    []string `json:"used_by,omitempty"`  // peer IDs that paired with it (a retry by one of them is free)
	EndedAt   int64    `json:"ended_at,omitempty"` // revoked or used up at
}

type peerRec struct {
	PeerID   string     `json:"peer_id"`
	Name     string     `json:"name"`
	Addrs    []string   `json:"addrs,omitempty"` // last known addresses
	Kind     InviteKind `json:"kind,omitempty"`
	GroupID  string     `json:"group_id,omitempty"`
	PairedAt int64      `json:"paired_at"`
	// Introduced: trusted through Trust (a co-member of a group vouched for by its admin)
	// rather than paired with this node through an invite.
	Introduced bool  `json:"introduced,omitempty"`
	LastSeen   int64 `json:"last_seen,omitempty"`
}

func loadState(path string) (*state, error) {
	s := &state{path: path, Peers: map[string]*peerRec{}}
	if path == "" {
		return s, nil
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, s); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	s.path = path
	if s.Peers == nil {
		s.Peers = map[string]*peerRec{}
	}
	return s, nil
}

// save writes the file atomically as a secret file (private to the user).
func (s *state) save() error {
	if s.path == "" {
		return nil
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return config.WriteSecretFile(s.path, append(b, '\n'))
}

// pruneInvites drops invites that ended (expired, revoked or used up) more than a day ago.
func (s *state) pruneInvites(now int64) bool {
	keep := s.Invites[:0]
	changed := false
	for _, in := range s.Invites {
		if in.endedAt(now) > 0 && now-in.endedAt(now) > int64(24*time.Hour/time.Millisecond) {
			changed = true
			continue
		}
		keep = append(keep, in)
	}
	s.Invites = keep
	return changed
}

// endedAt is when the invite stopped being usable (0 = still open).
func (in *inviteRec) endedAt(now int64) int64 {
	switch {
	case in.EndedAt > 0 && in.EndedAt < in.ExpiresAt:
		return in.EndedAt
	case in.ExpiresAt <= now:
		return in.ExpiresAt
	case in.Revoked, in.MaxUses > 0 && in.Uses >= in.MaxUses:
		return now // EndedAt not recorded (older file): count from now
	}
	return 0
}

func (in *inviteRec) open(now int64) bool { return in.endedAt(now) == 0 }
