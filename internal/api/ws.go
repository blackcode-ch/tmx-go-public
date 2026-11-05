package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/coder/websocket"

	"github.com/blackcode-ch/tmx-go/internal/fleet"
	"github.com/blackcode-ch/tmx-go/internal/model"
)

const writeTimeout = 10 * time.Second

// accept upgrades the request. Origins were already checked by guard (which also refuses
// foreign origins), so the library's same-origin check is not repeated here.
func accept(w http.ResponseWriter, r *http.Request) (*websocket.Conn, error) {
	// Negotiate only the public protocol, never echo the bearer credential.
	return websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true, Subprotocols: []string{"tmx"}})
}

func writeFrame(ctx context.Context, c *websocket.Conn, typ websocket.MessageType, b []byte) error {
	ctx, cancel := context.WithTimeout(ctx, writeTimeout)
	defer cancel()
	return c.Write(ctx, typ, b)
}

// events: a snapshot first, then BusEvents; a fresh snapshot whenever events were dropped.
func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	// Subscribe before the handshake completes: a client may act as soon as its Dial returns
	// (tmx wa link posts /link, whose QR event follows at once) and must not miss what follows.
	sub := s.f.Subscribe()
	defer s.f.Unsubscribe(sub)
	conn, err := accept(w, r)
	if err != nil {
		return
	}
	defer conn.CloseNow()
	ctx := conn.CloseRead(r.Context()) // we never read; this handles pings and close

	c, _ := caller(r)
	sendSnapshot := func() error {
		snap, err := s.f.Snapshot()
		if err != nil {
			return err
		}
		b, _ := json.Marshal(model.BusEvent{Type: model.EvSnapshot, Data: agentSnapshot(c, snap), At: model.Now()})
		return writeFrame(ctx, conn, websocket.MessageText, b)
	}
	if err := sendSnapshot(); err != nil {
		return
	}
	ping := time.NewTicker(30 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-ctx.Done():
			conn.Close(websocket.StatusNormalClosure, "")
			return
		case <-ping.C:
			pctx, cancel := context.WithTimeout(ctx, writeTimeout)
			err := conn.Ping(pctx)
			cancel()
			if err != nil {
				return
			}
		case ev := <-sub.C():
			if sub.TakeResync() {
				if sendSnapshot() != nil {
					return
				}
				continue
			}
			ev, ok := agentEvent(c, ev) // whatsapp.go: agents never get WhatsApp's words
			if !ok {
				continue
			}
			b, err := json.Marshal(ev)
			if err != nil {
				s.log.Error("marshal event", "type", ev.Type, "err", err)
				continue
			}
			if writeFrame(ctx, conn, websocket.MessageText, b) != nil {
				return
			}
		}
	}
}

type termInput struct {
	Type string `json:"type"` // input | resize
	Data string `json:"data"`
	Cols int    `json:"cols"`
	Rows int    `json:"rows"`
}

// terminal: replay first, then live output as binary frames; text frames carry input and
// resizes (owner only; agents may watch).
func (s *Server) terminal(w http.ResponseWriter, r *http.Request) {
	c, _ := caller(r)
	id := r.PathValue("id")
	sub, err := s.f.AttachTerminal(id)
	if err != nil {
		writeErr(w, err)
		return
	}
	defer s.f.DetachTerminal(sub)
	conn, err := accept(w, r)
	if err != nil {
		return
	}
	defer conn.CloseNow()
	conn.SetReadLimit(MaxBody)
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	if c.Owner {
		cols, _ := strconv.Atoi(r.URL.Query().Get("cols"))
		rows, _ := strconv.Atoi(r.URL.Query().Get("rows"))
		if cols > 0 && rows > 0 {
			_ = s.f.TerminalResize(c, id, cols, rows)
		}
	}
	go func() {
		defer cancel()
		for {
			typ, b, err := conn.Read(ctx)
			if err != nil {
				return
			}
			if typ != websocket.MessageText || !c.Owner {
				continue
			}
			var in termInput
			if json.Unmarshal(b, &in) != nil {
				continue
			}
			switch in.Type {
			case "input":
				err = s.f.TerminalInput(c, id, []byte(in.Data))
			case "resize":
				err = s.f.TerminalResize(c, id, in.Cols, in.Rows)
			}
			if err != nil && fleet.KindOf(err) == fleet.KindConflict {
				return // the process is gone
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
				conn.Close(websocket.StatusNormalClosure, "process ended")
				return
			}
			if err := writeFrame(ctx, conn, websocket.MessageBinary, b); err != nil {
				if !errors.Is(err, context.Canceled) {
					s.log.Debug("terminal write", "err", err)
				}
				return
			}
		}
	}
}
