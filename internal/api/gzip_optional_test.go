package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

// A plain JSON response must retain the original writer's optional capabilities.
type gzipNoFlushWriter struct {
	header http.Header
	body   []byte
	status int
}

func (w *gzipNoFlushWriter) Header() http.Header { return w.header }
func (w *gzipNoFlushWriter) WriteHeader(status int) { w.status = status }
func (w *gzipNoFlushWriter) Write(b []byte) (int, error) {
	w.body = append(w.body, b...)
	return len(b), nil
}

func TestGzipPlainOptionalFlusher(t *testing.T) {
	t.Run("nonflusher remains nonflusher", func(t *testing.T) {
		w := &gzipNoFlushWriter{header: make(http.Header)}
		h := gzipped(func(out http.ResponseWriter, _ *http.Request) {
			if _, ok := out.(http.Flusher); ok {
				t.Fatal("unexpected Flush capability")
			}
			_, _ = io.WriteString(out, "hi")
		})
		h(w, httptest.NewRequest(http.MethodGet, "/", nil))
		if w.Header().Get("Vary") != "Accept-Encoding" {
			t.Fatalf("Vary = %v", w.Header())
		}
	})
	t.Run("flusher retained and Vary committed", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h := gzipped(func(out http.ResponseWriter, _ *http.Request) {
			f, ok := out.(http.Flusher)
			if !ok {
				t.Fatal("lost original Flush capability")
			}
			out.Header().Set("Vary", "Origin")
			f.Flush()
		})
		h(rec, httptest.NewRequest(http.MethodGet, "/", nil))
		if got, want := rec.Result().Header.Values("Vary"), []string{"Origin", "Accept-Encoding"}; !reflect.DeepEqual(got, want) {
			t.Fatalf("Vary = %q; want %q", got, want)
		}
	})
}
