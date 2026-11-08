//go:build desktop

package desktop

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"

	"github.com/blackcode-ch/tmx-go/internal/model"
	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// SaveChatFile is called only by the owner's Download click. Webviews do not
// consistently support blob downloads; use an explicit native picker instead.
// The daemon verifies the bytes through the same owner-only read route as the UI.
func (a *App) SaveChatFile(chatID, id string) error {
	return a.saveChatFile(chatID, id, func(name string) (string, error) {
		return wruntime.SaveFileDialog(a.ctx, wruntime.SaveDialogOptions{Title: "Save chat file", DefaultFilename: name})
	})
}

func (a *App) saveChatFile(chatID, id string, choose func(string) (string, error)) error {
	raw, err := a.Request("GET", "/api/chats/"+url.PathEscape(chatID)+"/files/"+url.PathEscape(id)+"?data=1", "")
	if err != nil {
		return err
	}
	var file struct {
		model.ChatFile
		Data string `json:"data"`
	}
	if err := json.Unmarshal([]byte(raw), &file); err != nil {
		return err
	}
	data, err := base64.StdEncoding.DecodeString(file.Data)
	if err != nil {
		return err
	}
	if int64(len(data)) != file.Size {
		return errors.New("file download was incomplete")
	}
	path, err := choose(filepath.Base(file.Name))
	if err != nil || path == "" {
		return err
	}
	// Never auto-open, execute or overwrite a destination (including a symlink).
	dst, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, err = dst.Write(data)
	if e := dst.Close(); err == nil {
		err = e
	}
	if err != nil {
		_ = os.Remove(path)
	}
	return err
}
