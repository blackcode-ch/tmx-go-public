package config

import "testing"

func TestTrustedHostsConfig(t *testing.T) {
	for _, host := range []string{"", "*.example.ts.net", "https://trial.example.ts.net", "trial.example.ts.net/", ".example.ts.net", "trial..ts.net", "trial.example.ts.net.", "-trial.example.ts.net", "trial_.example.ts.net", "trial.example.ts.net:", "trial.example.ts.net:0", "trial.example.ts.net:65536", "trial.example.ts.net:08443", "trial.example.ts.net:+443", "trial.example.ts.net:443:8443", "trial.example.ts.net:https"} {
		if (APIConfig{TrustedHosts: []string{host}}).Validate() == nil {
			t.Errorf("accepted %q", host)
		}
	}
	for _, host := range []string{"trial.example.ts.net", "TRIAL.example.ts.net", "trial.example.ts.net:443", "trial.example.ts.net:8443", "trial.example.ts.net:65535"} {
		if err := (APIConfig{TrustedHosts: []string{host}}).Validate(); err != nil {
			t.Errorf("rejected %q: %v", host, err)
		}
	}
	p := testPaths(t)
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.API.TrustedHosts) != 0 {
		t.Fatal("default allowlist is not empty")
	}
	c.API.TrustedHosts = []string{"trial.example.ts.net:8443"}
	if err := Save(p, c); err != nil {
		t.Fatal(err)
	}
	c, err = Load(p)
	if err != nil || len(c.API.TrustedHosts) != 1 || c.API.TrustedHosts[0] != "trial.example.ts.net:8443" {
		t.Fatalf("round trip: %v, %v", c.API, err)
	}
	c.API.TrustedHosts = []string{"*.example.ts.net"}
	if err := Save(p, c); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(p); err == nil {
		t.Fatal("loaded wildcard config")
	}
}
