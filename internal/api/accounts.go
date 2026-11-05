package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/coder/websocket"

	"github.com/blackcode-ch/tmx-go/internal/accounts"
)

// AI accounts and usage (DESIGN.md "AI accounts and usage"): owner only, agents and Companion
// get 403. Without the service every route answers 501.
func (s *Server) accountRoutes(mux *http.ServeMux) {
	o := func(h http.HandlerFunc) http.HandlerFunc { return s.ownerOnly(s.withAccounts(h)) }
	mux.HandleFunc("GET /api/accounts", o(s.getAccounts))
	mux.HandleFunc("POST /api/accounts/refresh", o(s.refreshAccounts))
	mux.HandleFunc("POST /api/accounts/{harness}/login", o(s.accountLogin))
	mux.HandleFunc("DELETE /api/accounts/{harness}/login", o(s.cancelAccountLogin))
	mux.HandleFunc("GET /api/accounts/{harness}/login/terminal", o(s.accountLoginTerminal))
	mux.HandleFunc("POST /api/accounts/{harness}/logout", o(s.accountLogout))
	mux.HandleFunc("POST /api/accounts/{harness}/accounts", o(s.addAccount))
	mux.HandleFunc("DELETE /api/accounts/{harness}/accounts/{name}", o(s.removeAccount))
	mux.HandleFunc("PUT /api/accounts/{harness}/launch-default", o(s.setLaunchDefault))
}

// acctName is the ?account= of a sign-in route ("" = "default"): a harness's routes act on one
// of its accounts.
func acctName(r *http.Request) string { return r.URL.Query().Get("account") }

func (s *Server) withAccounts(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.accounts == nil {
			writeError(w, http.StatusNotImplemented, "accounts are not available")
			return
		}
		h(w, r)
	}
}

// writeAccountErr maps the accounts' errors: unknown harness 404, not installed 409, no
// sign-in 404.
func writeAccountErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, accounts.ErrUnknown), errors.Is(err, accounts.ErrNoLogin), errors.Is(err, accounts.ErrNoAccount):
		writeError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, accounts.ErrNotInstalled), errors.Is(err, accounts.ErrExists), errors.Is(err, accounts.ErrSigningIn),
		errors.Is(err, accounts.ErrTooMany), errors.Is(err, accounts.ErrNoSignOut):
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, accounts.ErrBadName), errors.Is(err, accounts.ErrIsDefault):
		writeError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, accounts.ErrNoProfiles):
		writeError(w, http.StatusNotImplemented, err.Error())
	default:
		writeError(w, http.StatusInternalServerError, err.Error())
	}
}

// GET /api/accounts
func (s *Server) getAccounts(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.accounts.Payload())
}

// POST /api/accounts/refresh {"harness": "claude"} (empty or no body = all) → 202 with the
// payload (refreshing: true); the results come as accounts.updated events.
func (s *Server) refreshAccounts(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Harness string `json:"harness"`
		Account string `json:"account"` // "" = every account of harness
	}
	b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxBody))
	if err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, "the request body is too large")
		return
	}
	if b = bytes.TrimSpace(b); len(b) > 0 {
		if err := json.Unmarshal(b, &req); err != nil {
			writeError(w, http.StatusBadRequest, "the request body is not valid JSON: "+err.Error())
			return
		}
	}
	if err := s.accounts.Refresh(req.Harness, req.Account); err != nil {
		writeAccountErr(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, s.accounts.Payload())
}

// POST /api/accounts/{harness}/login → {"session"}: starts the harness's sign-in in a terminal,
// or returns the running one. 409 when the harness is not installed.
func (s *Server) accountLogin(w http.ResponseWriter, r *http.Request) {
	id, err := s.accounts.Login(r.PathValue("harness"), acctName(r))
	if err != nil {
		writeAccountErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"session": id})
}

// DELETE /api/accounts/{harness}/login: cancels the running sign-in → the payload.
func (s *Server) cancelAccountLogin(w http.ResponseWriter, r *http.Request) {
	if err := s.accounts.CancelLogin(r.PathValue("harness"), acctName(r)); err != nil {
		writeAccountErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.accounts.Payload())
}

// POST /api/accounts/{harness}/logout: the harness's own sign-out → the payload.
func (s *Server) accountLogout(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	p, err := s.accounts.Logout(ctx, r.PathValue("harness"), acctName(r))
	if err != nil {
		writeAccountErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

// GET /api/accounts/{harness}/login/terminal?cols=&rows= (WebSocket), framed like an agent's
// terminal (ws.go terminal): binary frames of output, the replay first; text frames
// {"type":"input","data"} and {"type":"resize","cols","rows"}. The socket closes normally when
// the sign-in ends.
func (s *Server) accountLoginTerminal(w http.ResponseWriter, r *http.Request) {
	h, name := r.PathValue("harness"), acctName(r)
	sub, err := s.accounts.AttachLogin(h, name)
	if err != nil {
		writeAccountErr(w, err)
		return
	}
	defer s.accounts.DetachLogin(sub)
	conn, err := accept(w, r)
	if err != nil {
		return
	}
	defer conn.CloseNow()
	conn.SetReadLimit(MaxBody)
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	cols, _ := strconv.Atoi(r.URL.Query().Get("cols"))
	rows, _ := strconv.Atoi(r.URL.Query().Get("rows"))
	if cols > 0 && rows > 0 {
		_ = s.accounts.LoginResize(h, name, cols, rows)
	}
	go func() {
		defer cancel()
		for {
			typ, b, err := conn.Read(ctx)
			if err != nil {
				return
			}
			if typ != websocket.MessageText {
				continue
			}
			var in termInput
			if json.Unmarshal(b, &in) != nil {
				continue
			}
			switch in.Type {
			case "input":
				err = s.accounts.LoginInput(h, name, []byte(in.Data))
			case "resize":
				err = s.accounts.LoginResize(h, name, in.Cols, in.Rows)
			}
			if errors.Is(err, accounts.ErrNoLogin) {
				return // the sign-in is over
			}
		}
	}()

	if len(sub.Replay) > 0 {
		if writeFrame(ctx, conn, websocket.MessageBinary, sub.Replay) != nil {
			return
		}
	}
	for {
		select {
		case <-ctx.Done():
			conn.Close(websocket.StatusNormalClosure, "")
			return
		case b, ok := <-sub.C():
			if !ok {
				conn.Close(websocket.StatusNormalClosure, "sign-in ended")
				return
			}
			if err := writeFrame(ctx, conn, websocket.MessageBinary, b); err != nil {
				return
			}
		}
	}
}

// readName reads a {"name"} body.
func readName(w http.ResponseWriter, r *http.Request) (string, bool) {
	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, MaxBody)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, `the request body must be {"name": "..."}`)
		return "", false
	}
	return req.Name, true
}

// POST /api/accounts/{harness}/accounts {"name"}: adds a signed-out account; answers with the
// payload.
func (s *Server) addAccount(w http.ResponseWriter, r *http.Request) {
	name, ok := readName(w, r)
	if !ok {
		return
	}
	if err := s.accounts.AddAccount(r.PathValue("harness"), name); err != nil {
		writeAccountErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.accounts.Payload())
}

// DELETE /api/accounts/{harness}/accounts/{name}: takes an account off the list (its folder,
// and any sign-in in it, stays); answers with the payload.
func (s *Server) removeAccount(w http.ResponseWriter, r *http.Request) {
	if err := s.accounts.RemoveAccount(r.PathValue("harness"), r.PathValue("name")); err != nil {
		writeAccountErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.accounts.Payload())
}

// PUT /api/accounts/{harness}/launch-default {"name"}: the account of agents launched without
// one; answers with the payload.
func (s *Server) setLaunchDefault(w http.ResponseWriter, r *http.Request) {
	name, ok := readName(w, r)
	if !ok {
		return
	}
	if err := s.accounts.SetLaunchDefault(r.PathValue("harness"), name); err != nil {
		writeAccountErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.accounts.Payload())
}
