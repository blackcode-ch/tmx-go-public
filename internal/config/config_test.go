package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func testPaths(t *testing.T) Paths {
	t.Helper()
	t.Setenv(EnvHome, t.TempDir())
	p, err := Resolve()
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestHomeFromEnv(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(EnvHome, dir)
	h, err := DefaultHome()
	if err != nil || h != dir {
		t.Fatalf("home = %q, %v; want %q", h, err, dir)
	}
}

func TestLoadDefaultsAndSave(t *testing.T) {
	p := testPaths(t)
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.Port != DefaultPort || c.Machine == "" || c.Workspace == "" {
		t.Fatalf("defaults not filled: %+v", c)
	}
	if _, err := os.Stat(p.ConfigFile()); err != nil {
		t.Fatalf("config.json not written: %v", err)
	}
	c.Workspace = "~/elsewhere"
	c.Port = 9999
	if err := Save(p, c); err != nil {
		t.Fatal(err)
	}
	c2, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	home, _ := os.UserHomeDir()
	if c2.Port != 9999 || c2.Workspace != filepath.Join(home, "elsewhere") {
		t.Fatalf("reload: %+v", c2)
	}
	if got := c2.EffectivePolicy(); got.Levels["head"].Model != "claude-opus-5-5" {
		t.Fatalf("default policy: %+v", got)
	}
}

func TestOwnerToken(t *testing.T) {
	p := testPaths(t)
	t1, err := OwnerToken(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(t1) != 64 {
		t.Fatalf("token length %d", len(t1))
	}
	t2, err := OwnerToken(p)
	if err != nil || t1 != t2 {
		t.Fatalf("second read differs: %q %v", t2, err)
	}
	if runtime.GOOS != "windows" {
		st, _ := os.Stat(p.OwnerTokenFile())
		if st.Mode().Perm() != 0o600 {
			t.Fatalf("perm %v", st.Mode().Perm())
		}
	}
}

func TestDaemonInfoRoundTrip(t *testing.T) {
	p := testPaths(t)
	if _, err := ReadDaemonInfo(p); !os.IsNotExist(err) {
		t.Fatalf("want not-exist, got %v", err)
	}
	want := DaemonInfo{PID: 42, Port: 8787, StartedAt: 1, Version: "x"}
	if err := WriteDaemonInfo(p, want); err != nil {
		t.Fatal(err)
	}
	got, err := ReadDaemonInfo(p)
	if err != nil || got != want {
		t.Fatalf("got %+v %v", got, err)
	}
	if err := RemoveDaemonInfo(p); err != nil {
		t.Fatal(err)
	}
	if err := RemoveDaemonInfo(p); err != nil {
		t.Fatal(err)
	}
}

func TestLockIsExclusive(t *testing.T) {
	p := testPaths(t)
	l1, err := AcquireLock(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := AcquireLock(p); err != ErrLocked {
		t.Fatalf("second lock: %v, want ErrLocked", err)
	}
	if err := l1.Release(); err != nil {
		t.Fatal(err)
	}
	l3, err := AcquireLock(p)
	if err != nil {
		t.Fatalf("after release: %v", err)
	}
	l3.Release()
}

// A daemon restarting right after `tmx down` must wait for its predecessor's lock (21 Oct:
// `down` then `up` failed with ErrLocked while the old daemon was still stopping its agents).
func TestWaitLockWaitsForRelease(t *testing.T) {
	p := testPaths(t)
	l1, err := AcquireLock(p)
	if err != nil {
		t.Fatal(err)
	}
	go func() { time.Sleep(300 * time.Millisecond); l1.Release() }()
	l2, err := WaitLock(p, 5*time.Second)
	if err != nil {
		t.Fatalf("WaitLock: %v", err)
	}
	l2.Release()

	held, _ := AcquireLock(p)
	defer held.Release()
	start := time.Now()
	if _, err := WaitLock(p, 400*time.Millisecond); err != ErrLocked {
		t.Fatalf("held lock: %v, want ErrLocked", err)
	}
	if time.Since(start) < 400*time.Millisecond {
		t.Fatal("WaitLock gave up before its timeout")
	}
}

func TestP2PConfigDefaults(t *testing.T) {
	var none *P2PConfig
	if none.On() || none.DHTOn() || none.MDNSOn() || none.NATPortMapOn() {
		t.Fatal("nil p2p config must be private")
	}
	off := false
	c := &P2PConfig{Enabled: &off, DHT: &off}
	if c.On() || c.DHTOn() || c.MDNSOn() || c.NATPortMapOn() {
		t.Fatalf("explicit values: %+v", c)
	}
	var cfg Config
	if err := json.Unmarshal([]byte(`{"p2p":{"relays":["/ip4/1.2.3.4/tcp/4001/p2p/x"],"mdns":false,"nat_port_map":false}}`), &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.P2P.On() || cfg.P2P.MDNSOn() || cfg.P2P.NATPortMapOn() || cfg.P2P.DHTOn() || len(cfg.P2P.Relays) != 1 {
		t.Fatalf("parsed: %+v", cfg.P2P)
	}
}

func TestSupervisorConfigPreservesExplicitEmpty(t *testing.T) {
	p := testPaths(t)
	cfg, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Supervisor != nil {
		t.Fatal("absent supervisor should retain the default marker")
	}
	for _, name := range []string{"", "design-owner"} {
		cfg.Supervisor = &name
		if err = Save(p, cfg); err != nil {
			t.Fatal(err)
		}
		loaded, err := Load(p)
		if err != nil || loaded.Supervisor == nil || *loaded.Supervisor != name {
			t.Fatalf("supervisor round trip: %+v %v", loaded.Supervisor, err)
		}
	}
}
