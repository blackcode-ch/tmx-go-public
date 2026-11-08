package chat

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/blackcode-ch/tmx-go/internal/fleet"
	"github.com/blackcode-ch/tmx-go/internal/model"
)

func (s *Service) FileLimit() int64 {
	if s.o.FileMaxBytes > 0 && s.o.FileMaxBytes < MaxFileBytes {
		return s.o.FileMaxBytes
	}
	return MaxFileBytes
}

// An allowlist deliberately excludes scripts, executables, active web documents,
// macro-enabled Office documents and unknown extensions. Archives stay inert:
// neither this service nor the fetch tool unpacks or executes them.
func allowedFileName(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	return strings.Contains("|.txt|.md|.csv|.tsv|.json|.yaml|.yml|.xml|.log|.pdf|.rtf|.doc|.docx|.xls|.xlsx|.ppt|.pptx|.odt|.ods|.odp|.epub|.png|.jpg|.jpeg|.gif|.webp|.bmp|.tif|.tiff|.heic|.avif|.mp3|.wav|.ogg|.flac|.m4a|.aac|.opus|.mp4|.webm|.mov|.mkv|.zip|.tar|.gz|.bz2|.xz|.7z|.rar|", "|"+ext+"|") && ext != ""
}

func validateFile(name string, data []byte) error {
	if !allowedFileName(name) {
		return invalid("file type is blocked; use a document, image, audio/video or archive")
	}
	for _, magic := range [][]byte{[]byte("MZ"), []byte("\x7fELF"), []byte("#!"), {0xcf, 0xfa, 0xed, 0xfe}, {0xfe, 0xed, 0xfa, 0xcf}, {0xce, 0xfa, 0xed, 0xfe}, {0xfe, 0xed, 0xfa, 0xce}, {0xca, 0xfe, 0xba, 0xbe}} {
		if bytes.HasPrefix(data, magic) {
			return invalid("executable or script content is blocked")
		}
	}
	return nil
}

func blockedFileType(typ string) bool {
	switch strings.ToLower(cleanType(typ)) {
	case "text/html", "application/xhtml+xml", "image/svg+xml", "text/javascript", "application/javascript", "application/x-sh", "application/x-executable", "application/x-msdownload", "application/vnd.microsoft.portable-executable":
		return true
	}
	return false
}

func validateStoredFile(name, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	head := make([]byte, 512)
	n, err := f.Read(head)
	if err != nil && err != io.EOF {
		return err
	}
	return validateFile(name, head[:n])
}

// SendFile reuses the message authorization, rate limit, membership checks and
// existing file transport. The API supplies bytes from a confined agent root.
func (s *Service) SendFile(c fleet.Caller, chatID, name, caption string, data []byte) (model.ChatMessage, error) {
	if c.Owner || c.AgentID == "" || c.Peer != "" || c.Companion {
		return model.ChatMessage{}, forbidden("a local participating agent is required")
	}
	f, err := s.uploadFor(c, chatID, name, "", data)
	if err != nil {
		return model.ChatMessage{}, err
	}
	m, err := s.sendWith(c, f.ChatID, SendRequest{Body: caption, Files: []string{f.ID}}, true)
	if err != nil {
		// Only discard an upload that did not become a message attachment.
		s.mu.Lock()
		if current, e := s.st.GetChatFile(f.ChatID, f.ID); e == nil && current.MessageID == "" {
			_ = s.st.DeleteChatFile(f.ChatID, f.ID)
			_ = os.RemoveAll(s.fileDir(f.ChatID, f.ID))
		}
		s.mu.Unlock()
	}
	return m, err
}

func (s *Service) ReceivedFiles(c fleet.Caller, chatID string) ([]model.ChatFile, error) {
	if !s.Enabled() {
		return nil, ErrOff
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ch, err := s.chatForLocked(c, chatID)
	if err != nil {
		return nil, err
	}
	return s.st.ReceivedChatFiles(ch.ID)
}

// ReceivedFile resolves only files in chats visible to this caller. IDs are
// chat-scoped on the wire, so ambiguity fails closed instead of picking a peer.
func (s *Service) ReceivedFile(c fleet.Caller, id string) (model.ChatFile, []byte, error) {
	chats, err := s.Chats(c)
	if err != nil {
		return model.ChatFile{}, nil, err
	}
	var found model.ChatFile
	for _, ch := range chats {
		f, e := s.File(c, ch.ID, id)
		if e != nil || f.Sender == model.ChatMe || f.MessageID == "" {
			continue
		}
		if found.ID != "" {
			return model.ChatFile{}, nil, invalid("ambiguous file ID")
		}
		found = f
	}
	if found.ID == "" {
		return found, nil, notFound("no received file")
	}
	return s.ReadFile(c, found.ChatID, found.ID)
}
