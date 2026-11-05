// Package uploads keeps the files the owner attaches to messages. Each file lives at
// <state>/uploads/<agent id>/<upload id>/<name> next to a meta.json, so an agent reads it from a
// plain path (any harness can) and the folder goes away with the agent. Claude agents get
// --add-dir on their uploads folder so reading them needs no permission prompt.
package uploads

import (
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/blackcode-ch/tmx-go/internal/config"
	"github.com/blackcode-ch/tmx-go/internal/model"
)

const (
	// MaxFileBytes is the largest file one upload takes.
	MaxFileBytes = 20 << 20
	// MaxPerMessage is how many files one message carries.
	MaxPerMessage = 10
	maxNameRunes  = 120
	metaFile      = "meta.json"
)

var (
	ErrNotFound = errors.New("no such upload")
	ErrTooLarge = fmt.Errorf("the file is larger than %d MB", MaxFileBytes>>20)
	ErrEmpty    = errors.New("the file is empty")
)

// File describes one stored upload.
type File struct {
	ID        string `json:"id"`
	AgentID   string `json:"agent_id"`
	Name      string `json:"name"`
	Type      string `json:"type"`
	Size      int64  `json:"size"`
	Path      string `json:"path"`
	CreatedAt int64  `json:"created_at"`
}

// Image reports whether the file is an image (the UI shows a thumbnail).
func (f File) Image() bool { return strings.HasPrefix(f.Type, "image/") }

// Store is the uploads folder of one state directory.
type Store struct{ root string }

func New(p config.Paths) *Store { return &Store{root: p.UploadsDir()} }

// AgentDir is the folder an agent's uploads live in (and the one claude gets --add-dir for).
func (s *Store) AgentDir(agentID string) string { return filepath.Join(s.root, agentID) }

// OpenAgentRoot refuses a redirected per-agent directory, even a link to another
// agent within the uploads root. The returned handle stays on the checked inode.
func (s *Store) OpenAgentRoot(agentID string) (*os.Root, error) {
	if !validID(agentID) {
		return nil, ErrNotFound
	}
	root, err := os.OpenRoot(s.root)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	info, err := root.Lstat(agentID)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, os.ErrPermission
	}
	agent, err := root.OpenRoot(agentID)
	if err != nil {
		return nil, err
	}
	opened, err := agent.Stat(".")
	if err != nil || !os.SameFile(info, opened) {
		agent.Close()
		return nil, os.ErrPermission
	}
	return agent, nil
}

// Save stores data for agentID under a cleaned version of name. The type is taken from the
// content when the client sent none (or a generic one).
func (s *Store) Save(agentID, name, typ string, data []byte) (File, error) {
	if !validID(agentID) {
		return File{}, ErrNotFound
	}
	if len(data) == 0 {
		return File{}, ErrEmpty
	}
	if len(data) > MaxFileBytes {
		return File{}, ErrTooLarge
	}
	name = CleanName(name)
	typ = fileType(name, typ, data)
	f := File{ID: model.NewID(), AgentID: agentID, Name: name, Type: typ, Size: int64(len(data)), CreatedAt: model.Now()}
	if err := os.MkdirAll(s.root, 0o700); err != nil {
		return File{}, err
	}
	root, err := os.OpenRoot(s.root)
	if err != nil {
		return File{}, err
	}
	if err := root.MkdirAll(agentID, 0o700); err != nil {
		root.Close()
		return File{}, err
	}
	root.Close()
	root, err = s.OpenAgentRoot(agentID)
	if err != nil {
		return File{}, err
	}
	defer root.Close()
	dir := f.ID
	if err := root.Mkdir(dir, 0700); err != nil {
		return File{}, err
	}
	f.Path = filepath.Join(s.AgentDir(agentID), dir, name)
	if err := root.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
		_ = root.RemoveAll(dir)
		return File{}, err
	}
	b, _ := json.Marshal(f)
	// This is a new random directory; no reader receives its ID before metadata
	// is complete. Root-relative writes refuse a replaced agent-folder symlink.
	if err := root.WriteFile(filepath.Join(dir, metaFile), b, 0o600); err != nil {
		_ = root.RemoveAll(dir)
		return File{}, err
	}
	return f, nil
}

// Get returns the upload with id (of any agent).
func (s *Store) Get(id string) (File, error) {
	if !validID(id) {
		return File{}, ErrNotFound
	}
	matches, _ := filepath.Glob(filepath.Join(s.root, "*", id, metaFile))
	if len(matches) != 1 {
		return File{}, ErrNotFound
	}
	b, err := os.ReadFile(matches[0])
	if err != nil {
		return File{}, ErrNotFound
	}
	var f File
	if err := json.Unmarshal(b, &f); err != nil || f.ID != id {
		return File{}, ErrNotFound
	}
	// The stored path is rebuilt from the folder, never trusted from the file.
	f.Path = filepath.Join(filepath.Dir(matches[0]), f.Name)
	return f, nil
}

// Read returns an upload and its content.
func (s *Store) Read(id string) (File, []byte, error) {
	f, err := s.Get(id)
	if err != nil {
		return File{}, nil, err
	}
	b, err := os.ReadFile(f.Path)
	if err != nil {
		return File{}, nil, ErrNotFound
	}
	return f, b, nil
}

// RemoveAgent deletes every upload of an agent.
func (s *Store) RemoveAgent(agentID string) error {
	if !validID(agentID) {
		return nil
	}
	return os.RemoveAll(s.AgentDir(agentID))
}

// Prune deletes the uploads of agents that no longer exist.
func (s *Store) Prune(exists func(agentID string) bool) {
	entries, err := os.ReadDir(s.root)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() && !exists(e.Name()) {
			_ = os.RemoveAll(filepath.Join(s.root, e.Name()))
		}
	}
}

// Note is the text appended to a message that carries files: the agent reads them from the
// paths. The UI recognises the block (see ui/src/lib/attachments.ts) and shows the files.
func Note(files []File) string {
	if len(files) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(NoteHeader)
	for _, f := range files {
		fmt.Fprintf(&b, "\n- %s (%s, %s): %s", f.Name, f.Type, HumanSize(f.Size), f.Path)
	}
	return b.String()
}

// NoteHeader starts the attachment block of a message.
const NoteHeader = "Attached files (open them from these paths):"

// WithNote returns text followed by the note for files.
func WithNote(text string, files []File) string {
	note := Note(files)
	switch {
	case note == "":
		return text
	case strings.TrimSpace(text) == "":
		return note
	default:
		return strings.TrimRight(text, "\n") + "\n\n" + note
	}
}

// HumanSize prints a byte count the way the UI does.
func HumanSize(n int64) string {
	switch {
	case n < 1<<10:
		return fmt.Sprintf("%d B", n)
	case n < 1<<20:
		return fmt.Sprintf("%.0f KB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	}
}

// CleanName keeps a file name safe on every OS and readable in a message: no folders, no
// control or reserved characters, no line breaks (an agent reads its path on one line), no
// leading dots, at most 120 characters (extension kept).
func CleanName(name string) string {
	name = strings.ReplaceAll(name, "\\", "/")
	name = filepath.Base("/" + name)
	var b strings.Builder
	for _, r := range name {
		switch {
		case r == utf8.RuneError, unicode.IsControl(r), unicode.In(r, unicode.Zl, unicode.Zp), strings.ContainsRune(`<>:"/\|?*`, r):
			b.WriteRune('_')
		default:
			b.WriteRune(r)
		}
	}
	name = strings.TrimLeft(strings.TrimSpace(b.String()), ".")
	name = strings.TrimRight(name, ". ")
	if name == "" || name == "_" {
		name = "file"
	}
	if r := []rune(name); len(r) > maxNameRunes {
		ext := []rune(filepath.Ext(name))
		if len(ext) > 16 {
			ext = nil
		}
		name = string(r[:maxNameRunes-len(ext)]) + string(ext)
	}
	return name
}

// DetectType is the media type of a file: the client's type when specific, else from the
// name's extension, else from the content.
func DetectType(name, typ string, data []byte) string { return fileType(name, typ, data) }

func fileType(name, typ string, data []byte) string {
	typ = strings.TrimSpace(strings.ToLower(typ))
	if mt, _, err := mime.ParseMediaType(typ); err == nil && mt != "application/octet-stream" && strings.Contains(mt, "/") {
		return mt
	}
	if byExt := mime.TypeByExtension(strings.ToLower(filepath.Ext(name))); byExt != "" {
		if mt, _, err := mime.ParseMediaType(byExt); err == nil {
			return mt
		}
	}
	mt, _, _ := mime.ParseMediaType(http.DetectContentType(data))
	return mt
}

// validID accepts the IDs tmx makes (and nothing that could leave the uploads folder).
func validID(id string) bool {
	if id == "" || len(id) > 64 {
		return false
	}
	for _, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}
