package api_test

import (
	"github.com/blackcode-ch/tmx-go/internal/api"
	"github.com/blackcode-ch/tmx-go/internal/api/apitest"
	"github.com/blackcode-ch/tmx-go/internal/config"
	"net/http/httptest"
	"testing"
)

func TestSystemWarningsOwnerOnly(t *testing.T) {
	d := apitest.New(t)
	s := httptest.NewServer(api.New(api.Options{Fleet: d.Fleet, OwnerToken: d.OwnerToken, SystemWarnings: []string{config.TrialServicesWarning}}).Handler())
	defer s.Close()
	owner := client{t: t, base: s.URL, token: d.OwnerToken}
	var warnings []string
	if status := owner.do("GET", "/api/system/warnings", nil, &warnings); status != 200 || len(warnings) != 1 || warnings[0] != config.TrialServicesWarning {
		t.Fatalf("%d %v", status, warnings)
	}
	agent := client{t: t, base: s.URL, token: d.AgentToken(t, "head")}
	if status := agent.do("GET", "/api/system/warnings", nil, nil); status != 403 {
		t.Fatal(status)
	}
}
