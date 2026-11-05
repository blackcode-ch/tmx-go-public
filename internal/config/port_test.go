package config

import (
	"encoding/json"
	"os"
	"testing"
)

func TestPortConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name string
		file string
		env  string
		want int
		bad  bool
	}{
		{"default", "", "", 8797, false},
		{"configured", `{"port":8798}`, "", 8798, false},
		{"override", `{"port":8798}`, "8799", 8799, false},
		{"first override", "", "8799", 8799, false},
		{"override legacy", `{"port":8787}`, "8799", 8799, false},
		{"invalid text", "", "abc", 0, true},
		{"zero override", "", "0", 0, true},
		{"negative", `{"port":-1}`, "", 0, true},
		{"too high", "", "65536", 0, true},
		{"python", "", "8777", 0, true},
		{"existing http", `{"port":8787}`, "", 0, true},
		{"existing secondary", "", "43911", 0, true},
		{"existing p2p", "", "4001", 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := testPaths(t)
			t.Setenv(EnvPort, tc.env)
			if tc.file != "" {
				if err := os.WriteFile(p.ConfigFile(), []byte(tc.file), 0600); err != nil {
					t.Fatal(err)
				}
			}
			c, err := Load(p)
			if tc.bad {
				if err == nil {
					t.Fatal("accepted invalid port")
				}
				return
			}
			if err != nil || c.Port != tc.want {
				t.Fatalf("port=%d err=%v", c.Port, err)
			}
			b, err := os.ReadFile(p.ConfigFile())
			if err != nil {
				t.Fatal(err)
			}
			var stored Config
			if err := json.Unmarshal(b, &stored); err != nil {
				t.Fatal(err)
			}
			expected := DefaultPort
			if tc.file != "" {
				var original Config
				_ = json.Unmarshal([]byte(tc.file), &original)
				expected = original.Port
			}
			if stored.Port != expected {
				t.Fatalf("override persisted: %d, want %d", stored.Port, expected)
			}
		})
	}
}
