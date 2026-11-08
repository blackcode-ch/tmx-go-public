package config

import "testing"

func TestP2PFileLimitPersists(t *testing.T) {
	p := testPaths(t)
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	c.P2P = &P2PConfig{FileMaxBytes: 1024}
	if err := Save(p, c); err != nil {
		t.Fatal(err)
	}
	got, err := Load(p)
	if err != nil || got.P2P.FileLimit() != 1024 {
		t.Fatal(got.P2P, err)
	}
	for _, cfg := range []*P2PConfig{nil, {}, {FileMaxBytes: -1}, {FileMaxBytes: 26 << 20}} {
		if cfg.FileLimit() != 25<<20 {
			t.Fatal("invalid cap did not use protocol ceiling", cfg)
		}
	}
}
