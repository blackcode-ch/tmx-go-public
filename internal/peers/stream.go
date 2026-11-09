package peers

import (
	"context"
	"fmt"
	"github.com/blackcode-ch/tmx-go/internal/config"
	"net/http"
	"strings"
)

// ExportStream pulls an offered export using the configured peer URL and pair token.
// It never accepts a sender-supplied URL or follows redirects. The caller closes Body.
func (c *Client) ExportStream(ctx context.Context, host, ticket string, offset int64) (*http.Response, error) {
	return c.exportStream(ctx, host, "", ticket, offset)
}

// RelayExportStream uses Python's one-hop fleet export relay, preserving Range.
func (c *Client) RelayExportStream(ctx context.Context, via, source, ticket string, offset int64) (*http.Response, error) {
	if !config.ValidHostName(source) {
		return nil, fmt.Errorf("invalid source machine")
	}
	return c.exportStream(ctx, via, source, ticket, offset)
}
func (c *Client) exportStream(ctx context.Context, host, source, ticket string, offset int64) (*http.Response, error) {
	p, ok := c.peers[strings.ToLower(host)]
	if !ok {
		return nil, fmt.Errorf("unknown peer %s", host)
	}
	tok := c.tokens.For(p.Name)
	if tok == "" {
		return nil, fmt.Errorf("not paired with %s", host)
	}
	for _, ch := range ticket {
		if !(ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '_' || ch == '-') {
			return nil, fmt.Errorf("invalid ticket")
		}
	}
	if len(ticket) < 16 || len(ticket) > 64 {
		return nil, fmt.Errorf("invalid ticket")
	}
	path := "/api/exports/" + ticket
	if source != "" {
		path = "/api/relay/" + source + "/exports/" + ticket
	}
	req, err := http.NewRequestWithContext(ctx, "GET", p.URL+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set(HeaderToken, tok)
	req.Header.Set(HeaderFrom, c.machine)
	req.Header.Set(HeaderOnBehalf, "daemon")
	if offset > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
	}
	return c.http.Do(req)
}
