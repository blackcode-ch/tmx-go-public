package api

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/blackcode-ch/tmx-go/internal/fleet"
)

type P2PSendFileRequest struct {
	Chat    string `json:"chat" jsonschema:"chat ID or name"`
	Path    string `json:"path" jsonschema:"file in your own working folder (absolute or relative)"`
	Caption string `json:"caption,omitempty" jsonschema:"optional message, up to 32 KB"`
}
type P2PFilesRequest struct {
	Chat string `json:"chat" jsonschema:"chat ID or name"`
}
type P2PFetchFileRequest struct {
	ID   string `json:"id" jsonschema:"received file ID from p2p_files"`
	Dest string `json:"dest" jsonschema:"new file path in your working folder; parent must exist; never overwrites"`
}

func (s *Server) fileAgent(w http.ResponseWriter, r *http.Request) (fleet.Caller, bool) {
	c, _ := caller(r)
	if c.Owner || c.AgentID == "" || c.Companion || c.Peer != "" {
		writeError(w, 403, "a local participating agent is required")
		return c, false
	}
	if _, err := s.f.AgentForFiles(c, c.AgentID); err != nil {
		writeErr(w, err)
		return c, false
	}
	return c, true
}

func insidePath(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && (rel == "." || filepath.IsLocal(rel))
}

// Hold directory handles component by component. Lstat + SameFile refuses links
// (including a link substituted between check and open), and os.Root confines
// every open on Windows as well as Unix. Never resolve a destination via a shell.
func (s *Server) p2pFileParent(c fleet.Caller, path string) (*os.Root, string, error) {
	a, err := s.f.AgentForFiles(c, c.AgentID)
	if err != nil {
		return nil, "", err
	}
	if err := p2pWorkingFolderRule(a.Cwd, s.f.Paths().Home); err != nil {
		return nil, "", err
	}
	rootInfo, err := os.Lstat(a.Cwd)
	if err != nil || !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return nil, "", fileRule("path-containment")
	}
	base, err := filepath.EvalSymlinks(a.Cwd)
	if err != nil {
		return nil, "", fileRule("path-containment")
	}
	if err := p2pWorkingFolderRule(base, s.f.Paths().Home); err != nil {
		return nil, "", err
	}
	rel, err := relativeAgentPath(a.Cwd, path)
	if err != nil || rel == "." {
		return nil, "", fileRule("path-containment")
	}
	abs := filepath.Join(base, rel)
	home, err := filepath.EvalSymlinks(s.f.Paths().Home)
	if err != nil {
		return nil, "", fileRule("path-containment")
	}
	if insidePath(home, abs) {
		return nil, "", fileRule("path-containment")
	}

	parts := strings.Split(filepath.ToSlash(rel), "/")
	for _, part := range parts {
		if sensitiveFilePart(part) {
			return nil, "", fileRule("path-sensitive-name")
		}
	}
	root, err := os.OpenRoot(base)
	if err != nil {
		return nil, "", fileRule("path-unavailable")
	}
	openedInfo, err := root.Stat(".")
	if err != nil || !os.SameFile(rootInfo, openedInfo) {
		root.Close()
		return nil, "", fileRule("path-containment")
	}
	for _, part := range parts[:len(parts)-1] {
		before, err := root.Lstat(part)
		if err != nil || !before.IsDir() || before.Mode()&os.ModeSymlink != 0 {
			root.Close()
			return nil, "", fileRule("path-containment")
		}
		next, err := root.OpenRoot(part)
		root.Close()
		if err != nil {
			return nil, "", fileRule("path-unavailable")
		}
		after, err := next.Stat(".")
		if err != nil || !os.SameFile(before, after) {
			next.Close()
			return nil, "", fileRule("path-containment")
		}
		root = next
	}
	return root, parts[len(parts)-1], nil
}

func (s *Server) p2pSendFile(w http.ResponseWriter, r *http.Request) {
	c, ok := s.fileAgent(w, r)
	if !ok {
		return
	}
	var in P2PSendFileRequest
	if !decode(w, r, &in) {
		return
	}
	ch, err := s.chats.ChatFor(c, in.Chat)
	if err != nil {
		writeChatErr(w, err)
		return
	}
	root, name, err := s.p2pFileParent(c, in.Path)
	if err != nil {
		writeError(w, 403, err.Error())
		return
	}
	defer root.Close()
	before, err := root.Lstat(name)
	if err != nil || !before.Mode().IsRegular() {
		writeError(w, 403, fileRule("source-regular-file").Error())
		return
	}
	f, err := openAgentFile(root, name)
	if err != nil {
		writeError(w, 403, fileRule("file-unavailable").Error())
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || !os.SameFile(before, info) || !singleLinkFile(f) {
		writeError(w, 403, fileRule("source-changed-or-hardlinked").Error())
		return
	}
	if info.Size() > s.chats.FileLimit() {
		writeError(w, 413, "file-size-limit: refused; send a smaller non-sensitive document")
		return
	}
	data, err := io.ReadAll(io.LimitReader(f, s.chats.FileLimit()+1))
	if err != nil {
		writeError(w, 403, fileRule("file-unavailable").Error())
		return
	}
	if int64(len(data)) > s.chats.FileLimit() {
		writeError(w, 413, "file-size-limit: refused; send a smaller non-sensitive document")
		return
	}
	if err := p2pContentRule(data); err != nil {
		writeError(w, 403, err.Error())
		return
	}
	m, err := s.chats.SendFile(c, ch.ID, name, in.Caption, data)
	s.reply(w, 201, m, err)
}

func (s *Server) p2pFiles(w http.ResponseWriter, r *http.Request) {
	c, ok := s.fileAgent(w, r)
	if !ok {
		return
	}
	var in P2PFilesRequest
	if !decode(w, r, &in) {
		return
	}
	files, err := s.chats.ReceivedFiles(c, in.Chat)
	s.reply(w, 200, files, err)
}

func (s *Server) p2pFetchFile(w http.ResponseWriter, r *http.Request) {
	c, ok := s.fileAgent(w, r)
	if !ok {
		return
	}
	var in P2PFetchFileRequest
	if !decode(w, r, &in) {
		return
	}
	f, data, err := s.chats.ReceivedFile(c, in.ID)
	if err != nil {
		writeChatErr(w, err)
		return
	}
	root, name, err := s.p2pFileParent(c, in.Dest)
	if err != nil {
		writeError(w, 403, err.Error())
		return
	}
	defer root.Close()
	// Preserve the validated type; never turn a received document into a script.
	if !strings.EqualFold(filepath.Ext(name), filepath.Ext(f.Name)) {
		writeError(w, 400, "destination must preserve the file extension")
		return
	}
	dst, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		writeError(w, 403, fileRule("file-unavailable").Error())
		return
	}
	_, err = dst.Write(data)
	if e := dst.Close(); err == nil {
		err = e
	}
	if err != nil {
		_ = root.Remove(name)
		writeError(w, 403, fileRule("file-unavailable").Error())
		return
	}
	s.log.Info("chat file fetched", "name", f.Name, "size", f.Size, "sha256", f.SHA256)
	f.Path = in.Dest
	writeJSON(w, 201, f)
}
