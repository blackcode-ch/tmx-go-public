package api_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/api"
	"github.com/blackcode-ch/tmx-go/internal/api/apitest"
	"github.com/blackcode-ch/tmx-go/internal/hiddenexec"
	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/coder/websocket"
)

func fileAgent(t *testing.T, d *apitest.Daemon) model.Agent {
	t.Helper()
	o := client{t: t, base: d.URL, token: d.OwnerToken}
	var a model.Agent
	if code := o.do("POST", "/api/agents", map[string]any{"name": "files", "project_id": workerProject(t, d)}, &a); code != 201 {
		t.Fatal(code)
	}
	return a
}

func TestAgentFilesContainment(t *testing.T) {
	d := apitest.New(t)
	a := fileAgent(t, d)
	w := client{t: t, base: d.URL, token: d.AgentToken(t, a.ID)}
	if err := os.WriteFile(filepath.Join(a.Cwd, "report.txt"), []byte("report"), 0600); err != nil {
		t.Fatal(err)
	}
	if code := w.do("GET", "/api/sessions/files/files", nil, nil); code != 200 {
		t.Fatal(code)
	}
	var preview struct{ Text string }
	if code := w.do("GET", "/api/agents/files/file?path=report.txt", nil, &preview); code != 200 || preview.Text != "report" {
		t.Fatalf("preview: %d %+v", code, preview)
	}
	for _, path := range []string{"../outside", "..\\outside", "report.txt:secret", t.TempDir()} {
		if code := w.do("GET", "/api/agents/files/download?path="+url.QueryEscape(path), nil, nil); code != 403 {
			t.Fatalf("escape %q: %d", path, code)
		}
	}
	if code := w.do("GET", "/api/agents/head/files", nil, nil); code != 403 {
		t.Fatalf("other agent: %d", code)
	}
	req, _ := http.NewRequest("GET", d.URL+"/api/agents/files/download?path=report.txt", nil)
	req.Header.Set("Authorization", "Bearer "+w.token)
	req.Header.Set("Range", "bytes=1-3")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 206 || string(data) != "epo" || resp.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("download: %d %q", resp.StatusCode, data)
	}
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "private.txt"), []byte("private"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(a.Cwd, "escape")
	if runtime.GOOS == "windows" {
		if out, err := hiddenexec.Command("cmd.exe", "/d", "/c", "mklink", "/J", link, outside).CombinedOutput(); err != nil {
			t.Fatalf("junction: %v %s", err, out)
		}
	} else if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(link) })
	if code := w.do("GET", "/api/agents/files/download?path=escape/private.txt", nil, nil); code != 403 {
		t.Fatalf("symlink/junction escape: %d", code)
	}
}

func TestAgentImageChatAndAttach(t *testing.T) {
	d := apitest.New(t)
	a := fileAgent(t, d)
	o := client{t: t, base: d.URL, token: d.OwnerToken}
	w := client{t: t, base: d.URL, token: d.AgentToken(t, a.ID)}
	png := []byte("\x89PNG\r\n\x1a\n" + strings.Repeat("x", 100))
	body := map[string]string{"mime": "image/png", "data": base64.StdEncoding.EncodeToString(png)}
	if code := w.do("POST", "/api/agents/files/image", body, nil); code != 403 {
		t.Fatalf("agent image: %d", code)
	}
	var up upload
	if code := o.do("POST", "/api/agents/files/image", body, &up); code != 201 || !strings.HasPrefix(up.Path, d.Uploads.AgentDir(a.ID)+string(filepath.Separator)) {
		t.Fatalf("image: %d %+v", code, up)
	}
	body["mime"] = "image/jpeg"
	if code := o.do("POST", "/api/agents/files/image", body, nil); code != 415 {
		t.Fatalf("spoofed image: %d", code)
	}
	if code := w.do("GET", "/api/agents/files/chat?cursor=bad", nil, nil); code != 400 {
		t.Fatal(code)
	}
	if code := w.do("GET", "/api/agents/files/chat", nil, nil); code != 200 {
		t.Fatal(code)
	}
	d.Proc(t, a.ID).SetReplay([]byte("screen"))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ws := strings.Replace(d.URL, "http://", "ws://", 1) + "/api/sessions/files/attach?token=" + w.token
	conn, _, err := websocket.Dial(ctx, ws, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	_, data, err := conn.Read(ctx)
	if err != nil || string(data) != "screen" {
		t.Fatalf("attach replay: %s %v", data, err)
	}
}

func TestAgentDesktopRequiresLocalOwnerSession(t *testing.T) {
	d := apitest.New(t)
	a := fileAgent(t, d)
	var opened []string
	s := httptest.NewServer(api.New(api.Options{Fleet: d.Fleet, OwnerToken: d.OwnerToken, Port: 12345, OpenAgentDesktop: func(kind, target string) error { opened = append(opened, kind+":"+target); return nil }}).Handler())
	defer s.Close()
	request := func(path, token, ui string, forward bool, body any) int {
		b, _ := json.Marshal(body)
		r, _ := http.NewRequest("POST", s.URL+"/api/agents/files/"+path, bytes.NewReader(b))
		r.Header.Set("Authorization", "Bearer "+token)
		if ui != "" {
			r.Header.Set(api.UISessionHeader, ui)
		}
		if forward {
			r.Header.Set("X-Forwarded-For", "203.0.113.1")
		}
		resp, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}
	ui := strings.Repeat("a", 64)
	for _, action := range []string{"open", "reveal"} {
		if code := request(action, d.AgentToken(t, a.ID), ui, false, map[string]string{"path": "."}); code != 403 {
			t.Fatalf("agent %s: %d", action, code)
		}
		if code := request(action, d.OwnerToken, "", false, map[string]string{"path": "."}); code != 403 {
			t.Fatalf("no UI %s: %d", action, code)
		}
		if code := request(action, d.OwnerToken, ui, true, map[string]string{"path": "."}); code != 403 {
			t.Fatalf("proxy %s: %d", action, code)
		}
	}
	if code := request("reveal", d.OwnerToken, ui, false, map[string]string{"path": "../outside"}); code != 403 {
		t.Fatalf("reveal escape: %d", code)
	}
	if len(opened) != 0 {
		t.Fatal("denied request opened a window")
	}
	if code := request("open", d.OwnerToken, ui, false, nil); code != 200 || len(opened) != 1 || !strings.Contains(opened[0], "/#/agents/"+a.ID+"/terminal") {
		t.Fatalf("open: %d %v", code, opened)
	}
	if code := request("reveal", d.OwnerToken, ui, false, map[string]string{"path": "."}); code != 429 {
		t.Fatalf("rate limit: %d", code)
	}
	// A separate server has a fresh rate window; verify the successful reveal
	// target without invoking any OS process.
	// Resolve both paths because macOS temporary folders may use /var aliases.
	wantTarget, err := filepath.EvalSymlinks(a.Cwd)
	if err != nil {
		t.Fatal(err)
	}
	safe := httptest.NewServer(api.New(api.Options{Fleet: d.Fleet, OwnerToken: d.OwnerToken, Port: 12345, OpenAgentDesktop: func(kind, target string) error {
		resolvedTarget, err := filepath.EvalSymlinks(target)
		if err != nil {
			t.Errorf("resolve reveal target: %v", err)
			return err
		}
		if kind != "reveal" || resolvedTarget != wantTarget {
			t.Errorf("reveal target: %s %q", kind, target)
		}
		return nil
	}}).Handler())
	defer safe.Close()
	b := strings.NewReader(`{"path":"."}`)
	r, _ := http.NewRequest("POST", safe.URL+"/api/agents/files/reveal", b)
	r.Header.Set("Authorization", "Bearer "+d.OwnerToken)
	r.Header.Set(api.UISessionHeader, ui)
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("contained reveal: %d", resp.StatusCode)
	}
}
