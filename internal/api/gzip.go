package api

import (
	"compress/gzip"
	"net/http"
	"strings"
)

// gzipped compresses a handler's response when the client accepts gzip (DESIGN.md "Matrix",
// load budget: 5,000 thread cards are ~2.8 MB of JSON, ~14 kB gzipped). Used on plain
// request/response JSON routes only: never on streams, upgrades or ranged files.
func gzipped(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Upgrades are not a cached representation of this response. Leave
		// that bypass untouched: no compression and no header changes.
		if r.Header.Get("Upgrade") != "" {
			h(w, r)
			return
		}
		// The same URL is plain JSON for one client and gzip for another.
		// Advertise that on every response, including the ones we do not compress.
		addVaryAcceptEncoding(w.Header())
		if !acceptsGzip(r) {
			h(w, r)
			return
		}
		gw := &gzipWriter{ResponseWriter: w}
		defer gw.close()
		h(gw, r)
	}
}

// addVaryAcceptEncoding records that the representation varies on Accept-Encoding.
// A "*" already covers every request header. An Accept-Encoding token already
// present (any letter case, on any Vary line) is kept as-is, so existing lines
// are not rewritten and the token is not duplicated.
func addVaryAcceptEncoding(h http.Header) {
	for _, line := range h.Values("Vary") {
		for _, raw := range strings.Split(line, ",") {
			token := strings.TrimSpace(raw)
			if token == "*" || strings.EqualFold(token, "Accept-Encoding") {
				return
			}
		}
	}
	h.Add("Vary", "Accept-Encoding")
}

// acceptsGzip reports whether Accept-Encoding lists gzip without q=0.
func acceptsGzip(r *http.Request) bool {
	for _, part := range strings.Split(r.Header.Get("Accept-Encoding"), ",") {
		enc, params, _ := strings.Cut(strings.TrimSpace(part), ";")
		if strings.EqualFold(strings.TrimSpace(enc), "gzip") {
			q := strings.ReplaceAll(strings.TrimSpace(params), " ", "")
			return q != "q=0" && q != "q=0.0" && q != "q=0.00" && q != "q=0.000"
		}
	}
	return false
}

// gzipWriter compresses the body unless the response has none (204, 304) or is already encoded.
type gzipWriter struct {
	http.ResponseWriter
	zw     *gzip.Writer
	headed bool
}

func (g *gzipWriter) WriteHeader(code int) {
	if g.headed {
		return
	}
	g.headed = true
	if code != http.StatusNoContent && code != http.StatusNotModified && g.Header().Get("Content-Encoding") == "" {
		g.Header().Set("Content-Encoding", "gzip")
		g.Header().Del("Content-Length")
		g.zw = gzip.NewWriter(g.ResponseWriter)
	}
	g.ResponseWriter.WriteHeader(code)
}

func (g *gzipWriter) Write(b []byte) (int, error) {
	if !g.headed {
		g.WriteHeader(http.StatusOK)
	}
	if g.zw != nil {
		return g.zw.Write(b)
	}
	return g.ResponseWriter.Write(b)
}

func (g *gzipWriter) close() {
	if g.zw != nil {
		_ = g.zw.Close()
	}
}
