package api

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"github.com/blackcode-ch/tmx-go/internal/peers"
	"net/http"
	"strings"
)

func (s *Server) supervisorRef(host string) *peers.SupervisorRef {
	name, machine, _ := strings.Cut(s.f.SupervisorName(), "@")
	if name == "" {
		return nil
	}
	if machine == "" {
		machine = host
	}
	return &peers.SupervisorRef{Name: name, Host: machine}
}

// fleetRoster is an authenticated, metadata-only Python-shaped fleet view.
// Offline peers retain their last roster; hosts/online make staleness explicit.
func (s *Server) fleetRoster(w http.ResponseWriter, r *http.Request) {
	snap, err := s.f.Snapshot()
	if err != nil {
		writeErr(w, err)
		return
	}
	host := snap.Machine
	if s.peers != nil {
		host = s.peers.Machine()
	}
	projects := map[string]string{}
	for _, p := range snap.Projects {
		projects[p.ID] = p.Name
	}
	rows := []peers.Session{}
	for _, a := range snap.Agents {
		rows = append(rows, peers.Session{ID: a.ID, Name: a.Name, Host: host, State: pythonState(a.State),
			Project: projects[a.ProjectID], Role: a.Role, Title: a.Role, QueuedMessages: a.Queued, Rooms: []string{},
			Tier: string(a.Level), Cwd: a.Cwd, Adapter: string(a.Harness), ConversationID: a.SessionID})
	}
	hosts := []map[string]any{{"name": host, "online": true, "local": true, "supervisor": s.supervisorRef(host)}}
	if s.peers != nil {
		for _, peer := range s.peers.Statuses() {
			hosts = append(hosts, map[string]any{"name": peer.Name, "online": peer.OK, "error": peer.Error,
				"last_online": float64(peer.Seen) / 1000, "supervisor": peer.Supervisor})
			rows = append(rows, peer.Sessions...)
		}
	}
	value := map[string]any{"notice": LinkNotice, "host": host, "hosts": hosts, "sessions": rows, "supervisor": s.supervisorRef(host)}
	b, err := json.Marshal(value)
	if err != nil {
		writeErr(w, err)
		return
	}
	etag := fmt.Sprintf(`"%x"`, sha256.Sum256(b))
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", "no-store")
	for _, candidate := range strings.Split(r.Header.Get("If-None-Match"), ",") {
		candidate = strings.TrimSpace(candidate)
		if candidate == "*" || strings.TrimPrefix(candidate, "W/") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
	}
	writeJSON(w, http.StatusOK, value)
}
