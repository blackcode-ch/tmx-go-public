package config

import "testing"

func TestValidHostName(t *testing.T) {
	ok := []string{"a", "remote", "host-2", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}                                             // 40 chars
	bad := []string{"", "-bad", "Remote", "has_underscore", "../escape", "host/slash", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"} // 41 chars
	for _, n := range ok {
		if !ValidHostName(n) {
			t.Errorf("want valid: %q", n)
		}
	}
	for _, n := range bad {
		if ValidHostName(n) {
			t.Errorf("want invalid: %q", n)
		}
	}
}

func TestValidHostPort(t *testing.T) {
	// 8787 (this daemon's own default port) is deliberately allowed here: a Host may
	// legitimately be another local daemon running on it. config.go's own Load still refuses
	// it for c.Port (TestPortConfiguration), a different question.
	ok := []int{1024, 8787, 8797, 65535}
	bad := []int{0, 1, 22, 80, 443, 1023, 8777, 43911, 4001, 65536, -1}
	for _, p := range ok {
		if !ValidHostPort(p) {
			t.Errorf("want valid: %d", p)
		}
	}
	for _, p := range bad {
		if ValidHostPort(p) {
			t.Errorf("want invalid: %d", p)
		}
	}
}

func TestHostTokenFileRoundTrip(t *testing.T) {
	p := testPaths(t)
	name := "remote-1"
	if err := WriteSecret(p.HostTokenFile(name), "tok-123"); err != nil {
		t.Fatal(err)
	}
	got, err := ReadSecret(p.HostTokenFile(name))
	if err != nil || got != "tok-123" {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestConfigHostsRoundTrip(t *testing.T) {
	p := testPaths(t)
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	c.Hosts = []Host{{Name: "remote", Port: 8787}}
	if err := Save(p, c); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Hosts) != 1 || loaded.Hosts[0].Name != "remote" || loaded.Hosts[0].Port != 8787 {
		t.Fatalf("hosts: %+v", loaded.Hosts)
	}
}
