package peers

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Remote relays JSON to a configured, paired machine. The caller identity is
// supplied by the authenticated API handler, never by the request body.
// Successful Python relays return 200 even when the target returned 201/202.
func (c *Client) Remote(ctx context.Context, host, method, path, onBehalf string, body json.RawMessage) (int, json.RawMessage) {
	fail := func(status int, message, code string) (int, json.RawMessage) {
		b, _ := json.Marshal(map[string]string{"error": message, "code": code})
		return status, b
	}
	u, err := url.ParseRequestURI(path)
	if err != nil || !strings.HasPrefix(path, "/api/") || u.IsAbs() || u.Host != "" || u.Fragment != "" || strings.ContainsAny(path, "\\\r\n") {
		return fail(400, "remote path must start with /api/", "invalid_path")
	}
	method = strings.ToUpper(method)
	if method == "" {
		method = http.MethodGet
	}
	switch method {
	case "GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS":
	default:
		return fail(400, "invalid remote method", "invalid_method")
	}
	host = strings.ToLower(host)
	peer, ok := c.peers[host]
	if !ok {
		return fail(404, "no peer machine "+host, "not_found")
	}
	token := c.tokens.For(peer.Name)
	if token == "" {
		return fail(502, host+" is not paired with this machine", "peer_auth_failed")
	}
	if len(body) != 0 && !json.Valid(body) {
		return fail(400, "invalid JSON body", "invalid_body")
	}
	timeout := 30 * time.Second
	if method == "POST" && strings.HasSuffix(u.Path, "/files") {
		timeout = 300 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, peer.URL+path, bytes.NewReader(body))
	if err != nil {
		return fail(400, "invalid remote request", "invalid_request")
	}
	req.Header.Set(HeaderToken, token)
	req.Header.Set(HeaderFrom, c.machine)
	req.Header.Set(HeaderOnBehalf, onBehalf)
	if len(body) != 0 {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := c.http.Do(req)
	if err != nil {
		return fail(503, host+" is unreachable", "peer_unreachable")
	}
	defer res.Body.Close()
	b, err := io.ReadAll(io.LimitReader(res.Body, maxReply+1))
	if err != nil || len(b) > maxReply || !json.Valid(b) {
		return fail(502, host+" returned an invalid JSON response", "peer_response_invalid")
	}
	if res.StatusCode >= 300 {
		var detail map[string]json.RawMessage
		_ = json.Unmarshal(b, &detail)
		var code string
		_ = json.Unmarshal(detail["code"], &code)
		if res.StatusCode == 401 || (res.StatusCode == 403 && (code == "pair_required" || code == "not_approved")) {
			return fail(502, host+" refused this machine's credentials", "peer_auth_failed")
		}
		return res.StatusCode, b
	}
	return http.StatusOK, b
}
