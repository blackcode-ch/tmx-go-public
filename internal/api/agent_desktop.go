package api

import (
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/hiddenexec"
)

type agentDesktop struct {
	mu   sync.Mutex
	last time.Time
	open func(kind, target string) error
}

func (s *Server) allowAgentDesktop(w http.ResponseWriter, r *http.Request) bool {
	if !localOwner(r) || !atMachine(r) {
		writeError(w, 403, "opening a window requires the owner's session on this machine")
		return false
	}
	return true
}

func (s *Server) openAgentDesktop(w http.ResponseWriter, kind, target string) bool {
	s.agentDesktop.mu.Lock()
	if time.Since(s.agentDesktop.last) < 5*time.Second {
		s.agentDesktop.mu.Unlock()
		writeError(w, 429, "one desktop window every five seconds")
		return false
	}
	s.agentDesktop.last = time.Now()
	s.agentDesktop.mu.Unlock()
	if err := s.agentDesktop.open(kind, target); err != nil {
		writeError(w, 500, "could not open the desktop window")
		return false
	}
	return true
}

func (s *Server) agentOpen(w http.ResponseWriter, r *http.Request) {
	if !s.allowAgentDesktop(w, r) {
		return
	}
	a, err := s.f.Agent(r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	// Fixed local origin, no caller-controlled URL and no credential in the URL.
	target := fmt.Sprintf("http://127.0.0.1:%d/#/agents/%s/terminal", s.port, url.PathEscape(a.ID))
	if !s.openAgentDesktop(w, "open", target) {
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) agentReveal(w http.ResponseWriter, r *http.Request) {
	if !s.allowAgentDesktop(w, r) {
		return
	}
	var req struct {
		Path string `json:"path"`
	}
	if !decode(w, r, &req) {
		return
	}
	root, name, ok := s.agentFileRoot(w, r, req.Path)
	if !ok {
		return
	}
	defer root.Close()
	_, err := root.Stat(name)
	if err != nil {
		fileError(w, err)
		return
	}
	target, err := filepath.EvalSymlinks(filepath.Join(root.Name(), name))
	if err != nil {
		fileError(w, err)
		return
	}
	resolvedRoot, err := filepath.EvalSymlinks(root.Name())
	if err != nil {
		fileError(w, err)
		return
	}
	if _, err := relativeAgentPath(resolvedRoot, target); err != nil {
		fileError(w, err)
		return
	}
	if !s.openAgentDesktop(w, "reveal", target) {
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// These commands only open the fixed local UI or select a contained path. They
// never execute the selected file, interpolate a shell command, or carry tokens.
func openAgentDesktop(kind, target string) error {
	var program string
	var args []string
	switch runtime.GOOS {
	case "windows":
		program = filepath.Join(os.Getenv("SystemRoot"), "explorer.exe")
		if !filepath.IsAbs(program) {
			return fmt.Errorf("no system file manager")
		}
		if kind == "reveal" {
			args = []string{"/select," + target}
		} else {
			args = []string{target}
		}
	case "darwin":
		program = "/usr/bin/open"
		if kind == "reveal" {
			args = []string{"-R", target}
		} else {
			args = []string{target}
		}
	default:
		program = "xdg-open"
		if kind == "reveal" {
			if st, err := os.Stat(target); err != nil {
				return err
			} else if !st.IsDir() {
				target = filepath.Dir(target)
			}
		}
		args = []string{target}
	}
	cmd := hiddenexec.Command(program, args...)
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}
