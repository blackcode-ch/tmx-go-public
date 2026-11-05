package config

import (
	"encoding/json"
	"os"
	"testing"
)

func TestTrialWarningsPreserveServiceIsolation(t *testing.T) {
	for _, tc := range []struct {
		body    string
		warn    bool
		enabled bool
	}{
		{`{}`, false, false},
		{`{"whatsapp":{"enabled":true}}`, true, false},
		{`{"profile":"trial","whatsapp":{"enabled":true}}`, true, false},
		{`{"profile":"production","whatsapp":{"enabled":true}}`, false, true},
	} {
		t.Run(tc.body, func(t *testing.T) {
			p := Paths{Home: t.TempDir()}
			if err := os.WriteFile(p.ConfigFile(), []byte(tc.body), 0600); err != nil {
				t.Fatal(err)
			}
			c, err := Load(p)
			if err != nil {
				t.Fatal(err)
			}
			if (len(c.Warnings) > 0) != tc.warn || c.WhatsApp.On() != tc.enabled {
				t.Fatalf("%+v", c)
			}
			b, _ := json.Marshal(c)
			var m map[string]any
			_ = json.Unmarshal(b, &m)
			if _, ok := m["Warnings"]; ok {
				t.Fatal("runtime warnings persisted")
			}
		})
	}
	if len((Config{Peers: []Peer{{}}}).ProfileWarnings()) != 1 {
		t.Fatal("configured peers not warned")
	}
}
