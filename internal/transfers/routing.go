package transfers

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// Route discovers a single fleet relay using Python's /api/fleet roster. It never
// learns URLs or credentials from that response: calls still go to configured peers.
func (s *Service) Route(ctx context.Context, host string) (string, error) {
	if s.Peers.Known(host) {
		return "", nil
	}
	roster, ok := s.Peers.(interface{ Names() []string })
	if !ok {
		return "", fail(404, "unknown fleet machine")
	}
	type result struct {
		name  string
		found bool
	}
	names := roster.Names()
	ch := make(chan result, len(names))
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	for _, name := range names {
		go func(name string) {
			var out struct {
				Hosts []struct {
					Name   string `json:"name"`
					Online bool   `json:"online"`
				} `json:"hosts"`
			}
			found := false
			if s.Peers.Call(ctx, name, "GET", "/api/fleet", nil, &out) == nil {
				for _, h := range out.Hosts {
					if strings.EqualFold(h.Name, host) && h.Online {
						found = true
					}
				}
			}
			ch <- result{name, found}
		}(name)
	}
	for range names {
		select {
		case r := <-ch:
			if r.found {
				return r.name, nil
			}
		case <-ctx.Done():
			return "", fail(404, "no live fleet route to "+host)
		}
	}
	return "", fail(404, "no fleet route to "+host)
}
func (s *Service) call(ctx context.Context, host, method, path string, body, out any) error {
	via, err := s.Route(ctx, host)
	if err != nil {
		return err
	}
	if via == "" {
		return s.Peers.Call(ctx, host, method, path, body, out)
	}
	return s.Peers.Call(ctx, via, "POST", "/api/remote", Record{"host": host, "method": method, "path": path, "body": body}, out)
}

// StatusAt implements Python's optional ?host= on the transfer status route.
func (s *Service) StatusAt(ctx context.Context, host, id string) (Record, error) {
	if !idRx.MatchString(id) {
		return nil, fail(404, "no such transfer")
	}
	if host == "" || strings.EqualFold(host, s.Peers.Machine()) {
		return s.Status(ctx, id)
	}
	var out Record
	err := s.call(ctx, strings.ToLower(host), "GET", "/api/transfers/"+id, nil, &out)
	return out, err
}

type relayStreamer interface {
	RelayExportStream(context.Context, string, string, string, int64) (*http.Response, error)
}

func (s *Service) exportStream(ctx context.Context, r Record, offset int64) (*http.Response, error) {
	if via := r.str("via"); via != "" {
		p, ok := s.Peers.(relayStreamer)
		if !ok {
			return nil, fmt.Errorf("peer transport cannot relay exports")
		}
		return p.RelayExportStream(ctx, via, r.str("from"), r.str("ticket"), offset)
	}
	return s.Peers.ExportStream(ctx, r.str("from"), r.str("ticket"), offset)
}

var transferItemPath = regexp.MustCompile(`^/api/transfers/[0-9a-f]{16}$`)
var transferCancelPath = regexp.MustCompile(`^/api/transfers/[0-9a-f]{16}/cancel$`)
var exportDonePath = regexp.MustCompile(`^/api/exports/[A-Za-z0-9_-]{16,64}/done$`)

// RelayCall intentionally supports only this wire protocol, never arbitrary owner APIs.
func (s *Service) RelayCall(ctx context.Context, host, method, path string, body any) (Record, error) {
	allowed := method == "POST" && (path == "/api/transfers" || path == "/api/transfers/incoming" || transferCancelPath.MatchString(path) || exportDonePath.MatchString(path)) || method == "GET" && (path == "/api/transfers" || transferItemPath.MatchString(path))
	if !allowed || !s.Peers.Known(host) {
		return nil, fail(403, "only direct fleet transfer calls may be relayed")
	}
	var out Record
	err := s.Peers.Call(ctx, host, method, path, body, &out)
	return out, err
}
