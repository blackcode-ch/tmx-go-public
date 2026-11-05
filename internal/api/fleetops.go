package api

// Fleet operations for the head and the leads (the owner, 3 Nov 13:43: too many okays; tmx-lead
// 13:50): what an order from the fleet's leadership needs, WITHOUT the owner's token. Agents
// stay blocked from `tmx open` and tmx's state (the token would bypass every gate, including
// machine protection); instead:
//
//	GET  /api/fleet-ops/status   devices (id, name, created, expires), whether an owner PIN
//	                             is set (never the PIN or its hash), the gate's status
//	GET  /api/fleet-ops/log      the last lines of the daemon log, secrets redacted
//	POST /api/fleet-ops/restart  a graceful restart of this daemon (it starts itself again)
//
// Only the head and the leads; nothing here writes the gate, the PIN or the devices.

import (
	"bufio"
	"io"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/model"
)

const fleetOpsMaxLines = 500

func (s *Server) fleetOpsRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/fleet-ops/status", s.headOrLead(s.fleetOpsStatus))
	mux.HandleFunc("GET /api/fleet-ops/log", s.headOrLead(s.fleetOpsLog))
	mux.HandleFunc("POST /api/fleet-ops/restart", s.headOrLead(s.fleetOpsRestart))
}

// headOrLead admits this machine's head and leads (agent tokens), never the owner's token
// through here (the owner has the full API) and never Companion.
func (s *Server) headOrLead(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, ok := caller(r)
		if !ok || c.AgentID == "" || c.Owner || c.Companion || s.f == nil {
			writeError(w, http.StatusForbidden, "the head and the leads only")
			return
		}
		a, err := s.f.Agent(c.AgentID)
		if err != nil || (a.Level != model.LevelHead && a.Level != model.LevelLead) {
			writeError(w, http.StatusForbidden, "the head and the leads only")
			return
		}
		h(w, r)
	}
}

func (s *Server) fleetOpsStatus(w http.ResponseWriter, r *http.Request) {
	devices := []map[string]any{}
	if d := s.devices; d != nil && d.err == nil {
		d.mu.Lock()
		for _, v := range d.state.Devices {
			devices = append(devices, map[string]any{"id": v.ID, "name": v.Name, "created": v.Created, "expires": v.Expires, "recovery": v.Recovery})
		}
		d.mu.Unlock()
	}
	out := map[string]any{"devices": devices, "pin_set": s.pin != nil && s.pin.IsSet()}
	if s.gate != nil {
		st := s.gate.Status()
		out["gate"] = map[string]any{"enabled": st.Enabled, "available": st.Available, "method": st.Method}
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, out)
}

// logSecret matches what must never leave the log through an agent: bearer and pair tokens,
// "token=..." values, cookies, and any long hex or base64-ish run (session hashes, keys).
var logSecret = regexp.MustCompile(`(?i)(bearer\s+)\S+|((?:token|cookie|secret|password|pin|hash|salt)["']?\s*[=:]\s*["']?)[^\s"',}]+|\b[0-9a-f]{24,}\b|\b[A-Za-z0-9+/_-]{40,}={0,2}`)

// RedactLog replaces secrets in one log line.
func RedactLog(line string) string {
	return logSecret.ReplaceAllStringFunc(line, func(m string) string {
		sub := logSecret.FindStringSubmatch(m)
		switch {
		case sub[1] != "":
			return sub[1] + "[redacted]"
		case sub[2] != "":
			return sub[2] + "[redacted]"
		}
		return "[redacted]"
	})
}

func (s *Server) fleetOpsLog(w http.ResponseWriter, r *http.Request) {
	n, _ := strconv.Atoi(r.URL.Query().Get("lines"))
	if n <= 0 || n > fleetOpsMaxLines {
		n = 100
	}
	if s.f == nil {
		writeError(w, http.StatusServiceUnavailable, "no log")
		return
	}
	f, err := os.Open(s.f.Paths().DaemonLog())
	if err != nil {
		writeError(w, http.StatusNotFound, "no daemon log yet")
		return
	}
	defer f.Close()
	// Read at most the last 2 MB: enough for 500 lines, bounded memory.
	if st, err := f.Stat(); err == nil && st.Size() > 2<<20 {
		_, _ = f.Seek(-2<<20, io.SeekEnd)
	}
	var lines []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		lines = append(lines, sc.Text())
		if len(lines) > n {
			lines = lines[1:]
		}
	}
	for i := range lines {
		lines[i] = RedactLog(lines[i])
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{"lines": lines})
}

func (s *Server) fleetOpsRestart(w http.ResponseWriter, r *http.Request) {
	if s.selfRestart == nil {
		writeError(w, http.StatusNotImplemented, "this daemon cannot restart itself")
		return
	}
	c, _ := caller(r)
	s.log.Warn("daemon restart requested by an agent", "agent", c.AgentID)
	writeJSON(w, http.StatusAccepted, map[string]any{"restarting": true, "back_in": "about 15 seconds"})
	go func() {
		time.Sleep(500 * time.Millisecond) // let the answer reach the agent first
		s.selfRestart()
	}()
}
