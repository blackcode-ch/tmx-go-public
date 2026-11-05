package cli

import (
	"encoding/base64"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/api/apitest"
	"github.com/blackcode-ch/tmx-go/internal/config"
	"github.com/blackcode-ch/tmx-go/internal/fleet"
	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/p2p/p2ptest"
)

// useHome points the CLI at the install in home, served on port.
func useHome(t *testing.T, home string, port int) {
	t.Helper()
	t.Setenv(config.EnvHome, home)
	if err := config.WriteDaemonInfo(config.Paths{Home: home}, config.DaemonInfo{PID: 1, Port: port}); err != nil {
		t.Fatal(err)
	}
}

func urlPort(t *testing.T, raw string) int {
	t.Helper()
	u, _ := url.Parse(raw)
	_, port, _ := net.SplitHostPort(u.Host)
	n, err := strconv.Atoi(port)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func useDaemon(t *testing.T, d *apitest.Daemon) { t.Helper(); useHome(t, d.Home, urlPort(t, d.URL)) }

// eventually runs the command until check passes (the other install gets things async).
func eventually(t *testing.T, what string, check func() bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if check() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestChatCommandsBetweenTwoInstalls(t *testing.T) {
	nw := p2ptest.NewNetwork()
	a := apitest.NewWithNode(t, nw.NewNode("alex"))
	b := apitest.NewWithNode(t, nw.NewNode("bo"))

	useDaemon(t, a)
	if out, errs, code := tmx(t, "chat", "ls"); code != 0 || !strings.Contains(out, "no chats yet") {
		t.Fatalf("ls empty: %d %q %q", code, out, errs)
	}
	if out, errs, code := tmx(t, "chat", "new", "Weekend", "Trip"); code != 0 || !strings.Contains(out, "group Weekend Trip created") {
		t.Fatalf("new: %d %q %q", code, out, errs)
	}
	link, errs, code := tmx(t, "chat", "invite", "--hours", "2")
	if link = strings.TrimSpace(link); code != 0 || !strings.HasPrefix(link, "tmx://join/") || !strings.Contains(errs, "direct chat") {
		t.Fatalf("dm invite: %d %q %q", code, link, errs)
	}

	useDaemon(t, b)
	if out, errs, code := tmx(t, "chat", "join", link); code != 0 || !strings.Contains(out, "joined dm alex") || !strings.Contains(errs, "direct chat with alex") {
		t.Fatalf("join dm: %d %q %q", code, out, errs)
	}

	useDaemon(t, a)
	eventually(t, "alex lists the DM with bo", func() bool {
		out, _, _ := tmx(t, "chat", "ls")
		return regexp.MustCompile(`(?m)\bdm\s+bo\s`).MatchString(out)
	})
	if out, errs, code := tmx(t, "chat", "send", "BO", "lunch", "-x", "at", "12?"); code != 0 || !strings.Contains(out, "in bo:") {
		t.Fatalf("send: %d %q %q", code, out, errs)
	}
	if _, _, code := tmx(t, "chat", "send", "bo"); code != 2 {
		t.Fatalf("send without text: %d", code)
	}

	useDaemon(t, b)
	eventually(t, "bo reads alex's line", func() bool {
		out, _, _ := tmx(t, "chat", "read", "alex", "--limit", "5")
		return strings.Contains(out, "alex: lunch -x at 12?")
	})
	if out, _, _ := tmx(t, "chat", "ls"); !strings.Contains(out, "alex") || !strings.Contains(out, "head") {
		t.Fatalf("bo's ls (read marks it read; head takes part): %q", out)
	}
	if out, _, code := tmx(t, "chat", "send", "alex", "sure"); code != 0 || !strings.Contains(out, "message") {
		t.Fatalf("bo send: %d %q", code, out)
	}
	if out, _, _ := tmx(t, "chat", "read", "alex"); !strings.Contains(out, "(you): sure") {
		t.Fatalf("own line: %q", out)
	}

	// A group invite, joined from the other install.
	useDaemon(t, a)
	if _, errs, code := tmx(t, "chat", "invite", "bo"); code != 1 || !strings.Contains(errs, "is a DM") {
		t.Fatalf("group invite to a DM: %d %q", code, errs)
	}
	glink, errs, code := tmx(t, "chat", "invite", "weekend trip")
	if glink = strings.TrimSpace(glink); code != 0 || !strings.HasPrefix(glink, "tmx://join/") || !strings.Contains(errs, `"Weekend Trip"`) {
		t.Fatalf("group invite: %d %q %q", code, glink, errs)
	}
	useDaemon(t, b)
	if out, errs, code := tmx(t, "chat", "join", glink); code != 0 || !strings.Contains(out, "joined group Weekend Trip") {
		t.Fatalf("join group: %d %q %q", code, out, errs)
	}
	eventually(t, "bo sees both members", func() bool {
		out, _, _ := tmx(t, "chat", "members", "Weekend Trip")
		return strings.Contains(out, "alex") && strings.Contains(out, "admin") && strings.Contains(out, "you")
	})
	if out, _, code := tmx(t, "chat", "agent", "Weekend Trip", "none"); code != 0 || !strings.Contains(out, "people only") {
		t.Fatalf("agent none: %d %q", code, out)
	}
	if out, _, code := tmx(t, "chat", "agent", "Weekend Trip", "head"); code != 0 || !strings.Contains(out, "head takes part") {
		t.Fatalf("agent head: %d %q", code, out)
	}
	if _, errs, code := tmx(t, "chat", "agent", "Weekend Trip", "nobody"); code != 1 || !strings.Contains(errs, "no agent") {
		t.Fatalf("agent unknown: %d %q", code, errs)
	}

	// Names: a group called like the DM is ambiguous; the ID still works; unknown names fail.
	useDaemon(t, a)
	if _, _, code := tmx(t, "chat", "new", "Bo"); code != 0 {
		t.Fatal("new Bo")
	}
	if _, errs, code := tmx(t, "chat", "read", "bo"); code != 1 || !strings.Contains(errs, `2 chats are called "bo"`) {
		t.Fatalf("ambiguous: %d %q", code, errs)
	}
	var dmID string
	if list, err := a.Chats.Chats(fleet.OwnerCaller); err == nil {
		for _, c := range list {
			if c.Kind == model.ChatDM {
				dmID = c.ID
			}
		}
	}
	if out, errs, code := tmx(t, "chat", "read", dmID); code != 0 || !strings.Contains(out, "bo: sure") {
		t.Fatalf("read by id: %d %q %q", code, out, errs)
	}
	if _, errs, code := tmx(t, "chat", "read", "nope"); code != 1 || !strings.Contains(errs, `no chat "nope"`) {
		t.Fatalf("unknown: %d %q", code, errs)
	}

	// Leaving: the admin needs --yes; a member just leaves.
	if _, errs, code := tmx(t, "chat", "leave", "Weekend Trip"); code != 2 || !strings.Contains(errs, "--yes") {
		t.Fatalf("admin leave: %d %q", code, errs)
	}
	useDaemon(t, b)
	if out, errs, code := tmx(t, "chat", "leave", "Weekend Trip"); code != 0 || !strings.Contains(out, "left") {
		t.Fatalf("leave: %d %q %q", code, out, errs)
	}
	if _, _, code := tmx(t, "chat", "bogus"); code != 2 {
		t.Fatal("unknown chat command")
	}
}

// The files endpoint is being added separately: check the requests tmx chat send makes
// against a stub, and that read prints the files a message carries.
func TestChatSendFilesAndReadFiles(t *testing.T) {
	var mu sync.Mutex
	var uploads []map[string]string
	var sent []map[string]any
	chats := []model.Chat{{ID: "g1", Kind: model.ChatGroup, Name: "Ops", Status: model.ChatActive}}
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == "GET" && r.URL.Path == "/api/health":
			json.NewEncoder(w).Encode(map[string]any{"ok": true})
		case r.Method == "GET" && r.URL.Path == "/api/chats":
			json.NewEncoder(w).Encode(chats)
		case r.Method == "POST" && r.URL.Path == "/api/chats/g1/files":
			var up map[string]string
			json.NewDecoder(r.Body).Decode(&up)
			uploads = append(uploads, up)
			w.WriteHeader(201)
			json.NewEncoder(w).Encode(map[string]any{"id": "f" + strconv.Itoa(len(uploads)), "chat_id": "g1", "name": up["name"], "state": "ready"})
		case r.Method == "POST" && r.URL.Path == "/api/chats/g1/messages":
			var m map[string]any
			json.NewDecoder(r.Body).Decode(&m)
			sent = append(sent, m)
			w.WriteHeader(201)
			json.NewEncoder(w).Encode(model.ChatMessage{ID: "m1", ChatID: "g1", State: model.ChatSent})
		case r.Method == "GET" && r.URL.Path == "/api/chats/g1/messages":
			if r.URL.Query().Get("limit") != "30" {
				http.Error(w, `{"error":"limit"}`, 400)
				return
			}
			w.Write([]byte(`{"messages":[{"seq":1,"id":"m1","sender":"peerA","sender_name":"Alex","sender_agent":"head","body":"logs attached","at":1761000000000,
				"files":[{"id":"f1","name":"app.log","type":"text/plain","size":2048,"state":"ready","path":"/s/app.log"},
				{"id":"f2","name":"big.bin","type":"","size":5,"state":"pending"}],"mentions":[{"peer_id":"p-me","label":"me"}],"mentions_me":true}],"has_more":false}`))
		case r.Method == "POST" && r.URL.Path == "/api/chats/g1/read":
			w.Write([]byte(`{}`))
		default:
			http.Error(w, `{"error":"no such route"}`, 404)
		}
	}))
	defer stub.Close()
	useHome(t, t.TempDir(), urlPort(t, stub.URL))

	dir := t.TempDir()
	f1, f2 := filepath.Join(dir, "a.txt"), filepath.Join(dir, "b.png")
	os.WriteFile(f1, []byte("hello"), 0o600)
	os.WriteFile(f2, []byte{0x89, 'P', 'N', 'G'}, 0o600)
	if out, errs, code := tmx(t, "chat", "send", "ops", "-f", f1, "--file="+f2, "two", "files", "--", "-f", "x"); code != 0 || !strings.Contains(out, "message m1 in Ops: sent") {
		t.Fatalf("send -f: %d %q %q", code, out, errs)
	}
	mu.Lock()
	if len(uploads) != 2 || uploads[0]["name"] != "a.txt" || uploads[0]["data"] != base64.StdEncoding.EncodeToString([]byte("hello")) ||
		uploads[1]["name"] != "b.png" {
		t.Fatalf("uploads: %+v", uploads)
	}
	if _, ok := uploads[0]["type"]; !ok {
		t.Fatalf("upload lacks type: %+v", uploads[0])
	}
	if len(sent) != 1 || sent[0]["body"] != "two files -f x" || len(sent[0]["files"].([]any)) != 2 || sent[0]["files"].([]any)[1] != "f2" {
		t.Fatalf("message: %+v", sent)
	}
	mu.Unlock()

	// Files only: an empty body is fine. No files: just {"body"}.
	if _, errs, code := tmx(t, "chat", "send", "Ops", "-f", f1); code != 0 {
		t.Fatalf("files only: %d %q", code, errs)
	}
	if _, _, code := tmx(t, "chat", "send", "g1", "plain"); code != 0 {
		t.Fatal("plain send")
	}
	mu.Lock()
	if sent[1]["body"] != "" || len(sent[1]["files"].([]any)) != 1 {
		t.Fatalf("files-only message: %+v", sent[1])
	}
	if _, has := sent[2]["files"]; has || sent[2]["body"] != "plain" || len(sent[2]) != 1 {
		t.Fatalf("plain message: %+v", sent[2])
	}
	n := len(uploads)
	mu.Unlock()
	if _, _, code := tmx(t, "chat", "send", "Ops", "-f", filepath.Join(dir, "missing"), "x"); code == 0 {
		t.Fatal("missing file sent")
	}
	mu.Lock()
	if len(uploads) != n {
		t.Fatal("a missing file still uploaded the others")
	}
	mu.Unlock()

	out, errs, code := tmx(t, "chat", "read", "Ops")
	if code != 0 {
		t.Fatalf("read: %d %q", code, errs)
	}
	for _, want := range []string{"Alex/head: logs attached", "file: app.log (text/plain, 2.0 KB) /s/app.log", "file: big.bin (-, 5 B, pending)"} {
		if !strings.Contains(out, want) {
			t.Fatalf("read lacks %q:\n%s", want, out)
		}
	}
}

func TestSkillCommands(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if runtime.GOOS == "windows" {
		t.Setenv("USERPROFILE", home) // os.UserHomeDir reads USERPROFILE here
	}
	out, _, code := tmx(t, "skill")
	if code != 0 || !strings.HasPrefix(out, "---\nname: tmxg\ndescription: ") || !strings.Contains(out, "tmxg fleet trial") {
		t.Fatalf("skill: %d %q", code, out[:min(len(out), 200)])
	}
	if p, _, code := tmx(t, "skill", "path"); code != 0 || !strings.HasSuffix(strings.TrimSpace(p), filepath.Join(".claude", "skills", "tmxg", "SKILL.md")) {
		t.Fatalf("path: %d %q", code, p)
	}
	dir := filepath.Join(t.TempDir(), "skills", "tmxg")
	p, errs, code := tmx(t, "skill", "install", "--dir", dir)
	if code != 0 || strings.TrimSpace(p) != filepath.Join(dir, "SKILL.md") || strings.Contains(errs, "up to date") {
		t.Fatalf("install: %d %q %q", code, p, errs)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "SKILL.md")); string(b) != out {
		t.Fatal("installed content differs")
	}
	if _, errs, _ := tmx(t, "skill", "install", "--dir", dir); !strings.Contains(errs, "already up to date") {
		t.Fatalf("second install: %q", errs)
	}
	os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("old"), 0o644)
	if _, errs, _ := tmx(t, "skill", "install", "--dir", dir); strings.Contains(errs, "up to date") {
		t.Fatal("changed file not rewritten")
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "SKILL.md")); string(b) != out {
		t.Fatal("not overwritten")
	}
	file := filepath.Join(t.TempDir(), "afile")
	os.WriteFile(file, []byte("x"), 0o644)
	if _, errs, code := tmx(t, "skill", "install", "--dir", file); code != 1 || !strings.Contains(errs, "is a file") {
		t.Fatalf("dir is a file: %d %q", code, errs)
	}
	// The default goes under $HOME.
	if p, _, code := tmx(t, "skill", "install"); code != 0 || !strings.HasPrefix(strings.TrimSpace(p), os.Getenv("HOME")) {
		t.Fatalf("default install: %d %q", code, p)
	}
	if _, _, code := tmx(t, "skill", "bogus"); code != 2 {
		t.Fatal("bogus subcommand")
	}
}
