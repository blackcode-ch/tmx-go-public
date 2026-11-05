package api_test

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/blackcode-ch/tmx-go/internal/api"
	"github.com/blackcode-ch/tmx-go/internal/fleet"
	"github.com/blackcode-ch/tmx-go/internal/jobs"
)

func TestJobRecoveryFailureKeepsControlAvailable(t *testing.T) {
	for _, kind := range []string{"corrupt", "identity", "oversize", "unwritable"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "jobs.json")
			switch kind {
			case "corrupt":
				os.WriteFile(path, []byte("{"), 0600)
			case "identity":
				os.WriteFile(path, []byte(`[{"id":"wrong","identity":{"Caller":"owner","Owner":"owner","Key":"x"}}]`), 0600)
			case "oversize":
				f, err := os.Create(path)
				if err != nil {
					t.Fatal(err)
				}
				f.Truncate((8 << 20) + 1)
				f.Close()
			case "unwritable":
				os.WriteFile(path, []byte("not a directory"), 0600)
				path = filepath.Join(path, "jobs.json")
			}
			m, err := jobs.OpenFailSafe(jobs.Options{Path: path})
			if err == nil || m == nil || !m.Disabled() {
				t.Fatal("not disabled", err)
			}
			defer m.Close()
			srv := api.New(api.Options{Fleet: &fleet.Fleet{}, Jobs: m})
			response := httptest.NewRecorder()
			srv.Handler().ServeHTTP(response, httptest.NewRequest("GET", "http://localhost/api/health", nil))
			var v map[string]any
			json.Unmarshal(response.Body.Bytes(), &v)
			if response.Code != 200 || v["ok"] != true || v["jobs_disabled"] != true {
				t.Fatal(response.Code, response.Body.String())
			}
			again, err := jobs.OpenFailSafe(jobs.Options{Path: path})
			if err == nil || !again.Disabled() {
				t.Fatal("restarted empty after archive", err)
			}
			again.Close()
		})
	}
}
