package api

import (
	"encoding/json"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/blackcode-ch/tmx-go/internal/model"
)

var remoteSessionPath = regexp.MustCompile(`^/api/sessions/[a-zA-Z0-9][a-zA-Z0-9_.-]{0,79}/(screen|answer|keys|stop|start|restart|forget|open|rename)$`)
var remoteTaskPath = regexp.MustCompile(`^/api/tasks/[0-9a-f]{12,32}$`)
var remoteMessagePath = regexp.MustCompile(`^/api/messages/[0-9a-f]{12,32}(/(read|reply))?$`)

// Keep the relay to the Companion wire inventory. In particular, this is not a
// route around owner-only config, PIN, approval, process or credential endpoints.
func remotePathAllowed(method, raw string, owner bool) bool {
	u, err := url.ParseRequestURI(raw)
	if err != nil || u.IsAbs() || u.Host != "" || u.RawPath != "" || strings.ContainsAny(u.Path, "\\%") {
		return false
	}
	for _, part := range strings.Split(u.Path, "/") {
		if part == "." || part == ".." {
			return false
		}
	}
	queryKeys := ""
	allowed := false
	switch method + " " + u.Path {
	case "GET /api/health", "GET /api/fleet", "GET /api/sessions", "GET /api/tasks/capabilities":
		allowed = true
	case "GET /api/inbox":
		allowed, queryKeys = owner, "agent box unread limit offset"
	case "POST /api/messages", "POST /api/tasks":
		allowed = true
	case "POST /api/sessions":
		allowed = owner
	default:
		switch {
		case remoteTaskPath.MatchString(u.Path):
			allowed = method == "GET"
		case remoteMessagePath.MatchString(u.Path):
			allowed = owner && ((method == "GET" && !strings.HasSuffix(u.Path, "/read") && !strings.HasSuffix(u.Path, "/reply")) || (method == "POST" && (strings.HasSuffix(u.Path, "/read") || strings.HasSuffix(u.Path, "/reply"))))
		case remoteSessionPath.MatchString(u.Path):
			// Go cannot establish a local worker's subtree on another machine.
			// Session controls are therefore relayed only for the owner.
			allowed = owner && method == "POST" && !strings.HasSuffix(u.Path, "/screen")
			if strings.HasSuffix(u.Path, "/screen") {
				allowed, queryKeys = owner && method == "GET", "history"
			}
		}
	}
	values, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return false
	}
	for key, v := range values {
		if key == "" || len(v) != 1 || !strings.Contains(" "+queryKeys+" ", " "+key+" ") {
			return false
		}
	}
	return allowed
}

func (s *Server) remote(w http.ResponseWriter, r *http.Request) {
	c, ok := caller(r)
	if !ok || c.Companion || c.Peer != "" || (!c.Owner && c.AgentID == "") {
		writeError(w, http.StatusForbidden, "remote requires an authenticated fleet caller")
		return
	}
	onBehalf := "operator"
	if c.AgentID != "" {
		a, err := s.f.Agent(c.AgentID)
		if err != nil {
			writeErr(w, err)
			return
		}
		if a.Level != model.LevelHead && a.Level != model.LevelLead {
			writeError(w, http.StatusForbidden, "only the owner, head or lead may relay fleet requests")
			return
		}
		onBehalf = "agent:" + a.Name
	} else if id := s.deviceID(r); id != "" {
		onBehalf = "device:" + id
	}
	var req struct {
		Host   string          `json:"host"`
		Method string          `json:"method"`
		Path   string          `json:"path"`
		Body   json.RawMessage `json:"body"`
	}
	if !decode(w, r, &req) {
		return
	}
	req.Method = strings.ToUpper(req.Method)
	if req.Method == "" {
		req.Method = http.MethodGet
	}
	if !remotePathAllowed(req.Method, req.Path, c.Owner) {
		writeError(w, http.StatusForbidden, "this caller cannot relay that method or path")
		return
	}
	if s.peers == nil {
		writeError(w, http.StatusServiceUnavailable, "this daemon has no fleet peers")
		return
	}
	status, body := s.peers.Remote(r.Context(), req.Host, req.Method, req.Path, onBehalf, req.Body)
	writeJSON(w, status, body)
}
