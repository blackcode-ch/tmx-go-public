// Package client is a small HTTP client for the daemon API, used by the CLI (owner token)
// and the MCP server (agent token).
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Client struct {
	BaseURL string // e.g. http://127.0.0.1:8787
	Token   string
	HTTP    *http.Client
	Header  http.Header // extra headers on every request (nil = none)
}

func New(baseURL, token string) *Client {
	return &Client{BaseURL: strings.TrimRight(baseURL, "/"), Token: token,
		HTTP: &http.Client{Timeout: 60 * time.Second}}
}

// APIError is a non-2xx answer; Msg is the daemon's {error} sentence.
type APIError struct {
	Status int
	Msg    string
	Body   []byte // the raw answer (e.g. a 428's confirmation)
}

func (e *APIError) Error() string { return e.Msg }

// Do sends body (JSON, nil = none) and decodes the answer into out (nil = discard).
func (c *Client) Do(ctx context.Context, method, path string, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, rd)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	for k, v := range c.Header {
		req.Header[k] = v
	}
	hc := c.HTTP
	if hc == nil {
		hc = http.DefaultClient
	}
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(b, &e) != nil || e.Error == "" {
			e.Error = fmt.Sprintf("the daemon answered %s", resp.Status)
		}
		return &APIError{Status: resp.StatusCode, Msg: e.Error, Body: b}
	}
	if out != nil && len(b) > 0 {
		return json.Unmarshal(b, out)
	}
	return nil
}

func (c *Client) Get(ctx context.Context, path string, out any) error {
	return c.Do(ctx, http.MethodGet, path, nil, out)
}

func (c *Client) Post(ctx context.Context, path string, body, out any) error {
	return c.Do(ctx, http.MethodPost, path, body, out)
}

// Seg escapes one path segment (agent and project names, IDs).
func Seg(s string) string { return url.PathEscape(s) }
