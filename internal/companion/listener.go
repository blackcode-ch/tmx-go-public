package companion

import (
	"context"
	"errors"
	"net"
	"net/http"
	"path/filepath"
	"strconv"
	"time"
)

var ErrListenerIdentity = errors.New("Companion listener identity could not be verified; no key was sent")

// verifiedClient checks the accepted socket, not a previous probe of the port.
// No proxy and no connection reuse: every key-bearing connection is checked.
func verifiedClient() *http.Client {
	tr := &http.Transport{DisableKeepAlives: true}
	tr.DialContext = checkedDial((&net.Dialer{Timeout: 3 * time.Second}).DialContext, verifyCompanionConnection)
	return &http.Client{Transport: tr, Timeout: 20 * time.Second}
}

func checkedDial(dial func(context.Context, string, string) (net.Conn, error), verify func(net.Conn) error) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		c, err := dial(ctx, network, address)
		if err != nil {
			return nil, err
		}
		if err = verify(c); err != nil {
			c.Close()
			return nil, ErrListenerIdentity
		}
		return c, nil
	}
}

// The engine's advertised PID is necessary but not sufficient: the OS must bind
// the connected socket to it and its executable must be in the installed app.
func advertisedPID(port int) int {
	var engine struct {
		URL string `json:"url"`
		PID int    `json:"pid"`
	}
	if readJSON(filepath.Join(UserDataDir(), "agents", "engine.json"), &engine) {
		if u, e := CheckLoopback(engine.URL); e == nil {
			_, p, e := net.SplitHostPort(u[len("http://"):])
			if e == nil && p == strconv.Itoa(port) && engine.PID > 0 {
				return engine.PID
			}
		}
	}
	var state struct {
		Backend struct {
			Port int `json:"port"`
			PID  int `json:"pid"`
		} `json:"backend"`
	}
	if readJSON(filepath.Join(UserDataDir(), "local-first-engine-state.json"), &state) && state.Backend.Port == port && state.Backend.PID > 0 {
		return state.Backend.PID
	}
	return 0
}
