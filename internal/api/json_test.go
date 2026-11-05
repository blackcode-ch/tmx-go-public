package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// A large JSON reply must carry Content-Length and never fall back to Transfer-Encoding:
// chunked, which Python's HTTP client cannot read (found 4 Nov: Hostb's GET /api/sessions
// answer, once it had enough agents, went chunked and Hosta/Hostc saw Hostb as
// agent-less). json.NewEncoder(w).Encode(v) streams and leaves net/http to decide; writeJSON
// must marshal first instead. A real net/http round trip is required here - httptest.
// NewRecorder never chunks, so it cannot catch this.
func TestWriteJSONNeverChunks(t *testing.T) {
	big := make([]map[string]string, 2000)
	for i := range big {
		big[i] = map[string]string{"name": "agent-0000000000", "state": "idle", "title": "Buy two laptops online for the office"}
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, big)
	}))
	defer srv.Close()

	res, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()

	if res.ContentLength <= 0 {
		t.Fatalf("Content-Length missing or zero: %d (Transfer-Encoding %v)", res.ContentLength, res.TransferEncoding)
	}
	if len(res.TransferEncoding) != 0 {
		t.Fatalf("chunked: Transfer-Encoding %v (Content-Length %d)", res.TransferEncoding, res.ContentLength)
	}
}
