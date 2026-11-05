package companion_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blackcode-ch/tmx-go/internal/companion"
	"github.com/blackcode-ch/tmx-go/internal/companion/companiontest"
	"github.com/blackcode-ch/tmx-go/internal/model"
)

func TestClassifyEffects(t *testing.T) {
	cases := []struct {
		spec model.CompanionSpec
		want string
	}{
		{model.CompanionSpec{Goal: "Find the opening hours of the library"}, "none"},
		{model.CompanionSpec{Goal: "Pay the invoice from Swisscom"}, "money"},
		{model.CompanionSpec{Goal: "Check the price of the flight"}, "money"},
		{model.CompanionSpec{Goal: "Renew it", Steps: []string{"open the bank portal"}}, "money"},
		{model.CompanionSpec{Goal: "Log in to the school portal and download the report"}, "account"},
		{model.CompanionSpec{Goal: "Sign in to GitHub and read the notifications"}, "account"},
		{model.CompanionSpec{Goal: "Sign the contract in DocuSign"}, "outward"},
		{model.CompanionSpec{Goal: "Send the e-mail draft to the client"}, "outward"},
		{model.CompanionSpec{Goal: "Read the keyboard settings"}, "none"},             // "key" inside a word
		{model.CompanionSpec{Goal: "Summarise the paywall article"}, "none"},          // "pay" inside a word
		{model.CompanionSpec{Goal: "Look at the weather", Effects: "money"}, "money"}, // declared wins when stronger
		{model.CompanionSpec{Goal: "Buy two tickets", Effects: "none"}, "money"},      // found wins when stronger
		{model.CompanionSpec{Goal: "Look at the weather", Effects: "whatever"}, "money"},
		{model.CompanionSpec{Goal: "Costs in €"}, "money"},
		{model.CompanionSpec{Goal: "Check the SSL certificate of billing-portal.dev", Steps: []string{"open https://staging.billing-portal.dev/pay"}}, "none"},
		{model.CompanionSpec{Goal: "Read the README of the billing-service repo"}, "none"},
		{model.CompanionSpec{Goal: "Read ~/Projects/invoice/notes.txt"}, "none"},
		{model.CompanionSpec{Goal: "Then pay."}, "money"},
		{model.CompanionSpec{Goal: "Read the weather", DoNotTouch: []string{"do not log in", "no payments"}}, "none"},
		{model.CompanionSpec{Goal: "Write to alex@bank.example about it"}, "none"}, // the address is not the bank
	}
	for _, c := range cases {
		if got := companion.ClassifyEffects(c.spec); got != c.want {
			t.Errorf("%q (declared %q): %s, want %s", c.spec.Goal, c.spec.Effects, got, c.want)
		}
	}
}

func TestRenderAndScrub(t *testing.T) {
	spec := model.CompanionSpec{Goal: "Download the March statement", Context: "The login is in 1Password; password: hunter2",
		Steps: []string{"open the portal", "use key sk-abcdefghijklmnopqrstuvwx to call it"}, Success: "the PDF is in Downloads",
		DoNotTouch: []string{"the savings account", "settings"}, Where: "portal.example.ch"}
	eff := companion.ClassifyEffects(spec)
	text := companion.Render(spec, eff, companion.RenderInfo{Machine: "mac", Agent: "head", Origin: "agent"})
	for _, want := range []string{`asked by its agent "head"`, "Download the March statement", "Where: portal.example.ch",
		"1. open the portal", "2. use key", "Done when: the PDF is in Downloads", "Do not touch: the savings account; settings",
		"Effects: " + eff} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in\n%s", want, text)
		}
	}
	for _, secret := range []string{"hunter2", "sk-abcdefghijklmnopqrstuvwx"} {
		if strings.Contains(text, secret) {
			t.Errorf("secret %q not removed:\n%s", secret, text)
		}
	}
	// A git hash, a URL path and a UUID are no secrets.
	keep := "commit 0123456789abcdef0123456789abcdef01234567 at https://example.com/a/very/long/path/to/some/page-name-here id 123e4567-e89b-12d3-a456-426614174000"
	if got := companion.Scrub(keep); got != keep {
		t.Errorf("scrubbed too much:\n%s\n%s", keep, got)
	}
	if got := companion.Scrub("token AbCdEf0123456789AbCdEf0123456789xyz"); strings.Contains(got, "AbCdEf0123") {
		t.Errorf("long mixed key kept: %s", got)
	}
	owner := companion.Render(model.CompanionSpec{Goal: "x"}, "none", companion.RenderInfo{Machine: "mac", Agent: "head", Origin: "owner"})
	if !strings.HasPrefix(owner, "Task from the owner") || strings.Contains(owner, "Effects:") {
		t.Errorf("owner text: %s", owner)
	}
	if _, err := companion.CheckSpec(model.CompanionSpec{Goal: "  "}); err == nil {
		t.Error("empty goal accepted")
	}
	if _, err := companion.CheckSpec(model.CompanionSpec{Goal: "x", Effects: "lots"}); err == nil {
		t.Error("unknown effects accepted")
	}
}

func TestCheckLoopback(t *testing.T) {
	for _, ok := range []string{"http://127.0.0.1:8010", "http://localhost:9000/", "http://[::1]:8010"} {
		if _, err := companion.CheckLoopback(ok); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	for _, bad := range []string{"https://127.0.0.1:8010", "http://10.0.0.5:8010", "http://example.com:8010", "http://127.0.0.1:8010/a2a", "127.0.0.1:8010", "http://user@127.0.0.1:1"} {
		if _, err := companion.CheckLoopback(bad); err == nil {
			t.Errorf("%s accepted", bad)
		}
	}
}

func TestLocate(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TMXG_COMPANION_DATA", dir)
	if l, _ := companion.Locate(""); l.Source != companion.SourceDefault || l.URL != "http://127.0.0.1:8010" {
		t.Fatalf("default: %+v", l)
	}
	if _, err := companion.Locate("http://192.168.1.4:8010"); err == nil {
		t.Fatal("a LAN address was accepted")
	}
	write := func(name string, v any) {
		b, _ := json.Marshal(v)
		_ = os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o700)
		if err := os.WriteFile(filepath.Join(dir, name), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("local-first-engine-state.json", map[string]any{"backend": map[string]any{"port": 8123, "pid": os.Getpid()},
		"control": map[string]any{"token": "secret-never-read"}})
	if l, _ := companion.Locate(""); l.Source != companion.SourceEngineState || l.URL != "http://127.0.0.1:8123" {
		t.Fatalf("engine state: %+v", l)
	}
	write("agents/engine.json", map[string]any{"url": "http://127.0.0.1:8124", "pid": 999999999}) // dead pid: ignored
	if l, _ := companion.Locate(""); l.Source != companion.SourceEngineState {
		t.Fatalf("dead engine.json used: %+v", l)
	}
	write("agents/engine.json", map[string]any{"url": "http://127.0.0.1:8124", "pid": os.Getpid()})
	if l, _ := companion.Locate(""); l.Source != companion.SourceEngine || l.URL != "http://127.0.0.1:8124" {
		t.Fatalf("engine.json: %+v", l)
	}
	if l, _ := companion.Locate("http://127.0.0.1:9999"); l.Source != companion.SourceConfig {
		t.Fatalf("config: %+v", l)
	}
}

func TestInstallUninstall(t *testing.T) {
	aios := filepath.Join(t.TempDir(), "aios")
	exe := filepath.Join(t.TempDir(), "tmx")
	o := companion.InstallOptions{AIOSHome: aios, Executable: exe, URL: "http://127.0.0.1:18831", StateHome: "/state"}
	if _, err := companion.Install(o); err == nil || !strings.Contains(err.Error(), "not been set up") {
		t.Fatalf("install without Companion's folder: %v", err)
	}
	if err := os.MkdirAll(aios, 0o755); err != nil {
		t.Fatal(err)
	}
	st, err := companion.Install(o)
	if err != nil {
		t.Fatal(err)
	}
	if !st.Installed || !st.Current || !st.Skill || !st.Enabled || st.Foreign {
		t.Fatalf("status after install: %+v", st)
	}
	var e map[string]any
	b, _ := os.ReadFile(filepath.Join(aios, "mcps", "tmxg", "mcp.json"))
	if err := json.Unmarshal(b, &e); err != nil {
		t.Fatal(err)
	}
	if e["type"] != "command" || e["command"] != exe || e["managed_by"] != "tmxg" || e["runtime"] != nil {
		t.Fatalf("entry: %v", e)
	}
	if args := fmt.Sprint(e["args"]); args != "[mcp --companion]" {
		t.Fatalf("args: %s", args)
	}
	env := e["env"].(map[string]any)
	if env["TMXG_URL"] != "http://127.0.0.1:18831" || env["TMXG_HOME"] != "/state" || len(env) != 2 {
		t.Fatalf("env (no token in it): %v", env)
	}
	if strings.Contains(string(b), "token") {
		t.Fatalf("the entry carries a token: %s", b)
	}
	// The owner switched it off in Companion; a reinstall (new binary) keeps that.
	e["enabled"] = false
	b, _ = json.Marshal(e)
	_ = os.WriteFile(filepath.Join(aios, "mcps", "tmxg", "mcp.json"), b, 0o644)
	o2 := o
	o2.Executable = filepath.Join(t.TempDir(), "tmx2")
	if st, _ := companion.ReadInstall(o2); st.Current || !st.Installed {
		t.Fatalf("a moved binary should read as installed but not current: %+v", st)
	}
	st, err = companion.Install(o2)
	if err != nil || !st.Current || st.Enabled {
		t.Fatalf("reinstall: %+v %v", st, err)
	}
	st, err = companion.Uninstall(o2)
	if err != nil || st.Installed || st.Skill {
		t.Fatalf("uninstall: %+v %v", st, err)
	}
	if _, err := os.Stat(filepath.Join(aios, "mcps", "tmxg")); !os.IsNotExist(err) {
		t.Fatal("tmx's folder left behind")
	}
}

func TestInstallLeavesForeignEntriesAlone(t *testing.T) {
	aios := t.TempDir()
	o := companion.InstallOptions{AIOSHome: aios, Executable: filepath.Join(t.TempDir(), "tmx"), URL: "http://127.0.0.1:1", StateHome: "/s"}
	foreign := []byte(`{"name":"Someone's tmx","type":"url","enabled":true,"url":"https://tmx.example/mcp"}`)
	_ = os.MkdirAll(filepath.Join(aios, "mcps", "tmxg"), 0o755)
	_ = os.WriteFile(filepath.Join(aios, "mcps", "tmxg", "mcp.json"), foreign, 0o644)
	if _, err := companion.Install(o); !errors.Is(err, companion.ErrForeign) {
		t.Fatalf("install over a foreign entry: %v", err)
	}
	if st, _ := companion.Uninstall(o); !st.Foreign {
		t.Fatalf("status: %+v", st)
	}
	if b, _ := os.ReadFile(filepath.Join(aios, "mcps", "tmxg", "mcp.json")); string(b) != string(foreign) {
		t.Fatalf("foreign entry changed: %s", b)
	}
	// A foreign skill blocks too, and survives an uninstall.
	aios2 := t.TempDir()
	o.AIOSHome = aios2
	_ = os.MkdirAll(filepath.Join(aios2, "skills", "tmxg"), 0o755)
	_ = os.WriteFile(filepath.Join(aios2, "skills", "tmxg", "SKILL.md"), []byte("---\nname: tmx\n---\nmine"), 0o644)
	if _, err := companion.Install(o); !errors.Is(err, companion.ErrForeign) {
		t.Fatalf("install over a foreign skill: %v", err)
	}
	if _, err := companion.Uninstall(o); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(aios2, "skills", "tmxg", "SKILL.md")); string(b) != "---\nname: tmx\n---\nmine" {
		t.Fatalf("foreign skill changed: %s", b)
	}
	if _, err := os.Stat(filepath.Join(aios2, "mcps", "tmxg")); !os.IsNotExist(err) {
		t.Fatal("install wrote its entry despite the foreign skill")
	}
}

func TestClientAgainstFake(t *testing.T) {
	f := companiontest.New(t, "key-1")
	c := &companion.Client{HTTP: http.DefaultClient, BaseURL: f.URL, Token: "key-1", Agent: "tmx/test"}
	ctx := context.Background()
	card, err := c.Card(ctx)
	if err != nil || card.Name != "Companion" || card.Version == "" {
		t.Fatalf("card: %+v %v", card, err)
	}
	if me, err := c.Me(ctx); err != nil || me.Label != "tmx" {
		t.Fatalf("me: %+v %v", me, err)
	}
	task, err := c.Send(ctx, "hello", "", "")
	if err != nil || task.ID == "" || task.Status.State != companion.StateSubmitted {
		t.Fatalf("send: %+v %v", task, err)
	}
	if _, err := c.Send(ctx, "second", "", ""); companion.AsError(err) == nil || companion.AsError(err).Reason != companion.ReasonBusy {
		t.Fatalf("second send while busy: %v", err)
	}
	f.Ask(task.ID, "Which account?")
	got, err := c.Get(ctx, task.ID)
	if err != nil || got.Status.State != companion.StateInputRequired || got.Question() != "Which account?" {
		t.Fatalf("question: %+v %v", got, err)
	}
	if _, err := c.Send(ctx, "the private one", task.ID, got.ContextID); err != nil {
		t.Fatalf("answer: %v", err)
	}
	f.Complete(task.ID, "Done: downloaded.")
	got, _ = c.Get(ctx, task.ID)
	if got.Status.State != companion.StateCompleted || got.Result() != "Done: downloaded." || got.FirstUserText() != "hello" {
		t.Fatalf("result: %+v", got)
	}
	bad := &companion.Client{HTTP: http.DefaultClient, BaseURL: f.URL, Token: "wrong"}
	_, err = bad.Me(ctx)
	if e := companion.AsError(err); e == nil || e.Status != 401 || !strings.Contains(e.Msg, "does not recognise this key") {
		t.Fatalf("bad key: %v", err)
	}
	if _, err := c.Get(ctx, "nope"); companion.AsError(err) == nil || companion.AsError(err).Status != 404 {
		t.Fatalf("missing task: %v", err)
	}
	// Nothing listening: unreachable (so nothing was sent).
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	ln.Close()
	_, err = (&companion.Client{BaseURL: "http://" + addr, Token: "k"}).Send(ctx, "x", "", "")
	if e := companion.AsError(err); e == nil || !e.Unreachable || e.Unconfirmed {
		t.Fatalf("closed port: %+v", e)
	}
	// Never off this machine.
	if _, err := (&companion.Client{BaseURL: "http://10.1.2.3:8010", Token: "k"}).Card(ctx); err == nil {
		t.Fatal("a LAN address was contacted")
	}
}
