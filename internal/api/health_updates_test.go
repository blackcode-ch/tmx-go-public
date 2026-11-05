package api_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/blackcode-ch/tmx-go/internal/api/apitest"
	"github.com/blackcode-ch/tmx-go/internal/config"
)

// GET /api/health says whether updates are off because of invalid pinned keys: a bool, no reason.
func TestHealthUpdatesOK(t *testing.T) {
	d := apitest.New(t)
	health := func(url string) map[string]any {
		t.Helper()
		resp, err := http.Get(url + "/api/health")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var v map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	if _, ok := health(d.URL)["updates_ok"]; ok {
		t.Fatal("updates_ok without an updater")
	}
	url, _ := d.WithUpdater(t)
	if v := health(url); v["updates_ok"] != true {
		t.Fatalf("no keys pinned: %v", v)
	}
	d.SetUpdateSettings(config.UpdateConfig{FleetKeys: []config.FleetKey{
		{ID: "fleet-release-v1", Type: config.KeyEd25519, Key: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="}}})
	if v := health(url); v["updates_ok"] != true {
		t.Fatalf("a valid key: %v", v)
	}
	d.SetUpdateSettings(config.UpdateConfig{FleetKeys: []config.FleetKey{{ID: "fleet-release-v1", Type: config.KeyEd25519, Key: "bm90LWEta2V5"}}})
	v := health(url)
	if v["updates_ok"] != false {
		t.Fatalf("an invalid key: %v", v)
	}
	if _, ok := v["updates_reason"]; ok {
		t.Fatal("health must not say why")
	}
	d.SetUpdateSettings(config.UpdateConfig{Require: []string{config.KeyWebAuthn}})
	if v := health(url); v["updates_ok"] != false {
		t.Fatalf("require without its key: %v", v)
	}
}
