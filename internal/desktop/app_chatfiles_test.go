//go:build desktop

package desktop

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestSaveChatFile(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer fixture" || r.URL.Path != "/api/chats/chat/files/file" || r.URL.Query().Get("data") != "1" {
			t.Error("wrong file request")
			w.WriteHeader(403)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"name": "report.txt", "size": 6, "data": base64.StdEncoding.EncodeToString([]byte("report"))})
	}))
	defer server.Close()
	a := &App{ctx: context.Background(), opts: Options{URL: server.URL, Token: "fixture"}, client: server.Client()}
	path := filepath.Join(t.TempDir(), "saved.txt")
	choose := func(name string) (string, error) {
		if name != "report.txt" {
			t.Error(name)
		}
		return path, nil
	}
	if err := a.saveChatFile("chat", "file", choose); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "report" {
		t.Fatal("download", err)
	}
	if err := a.saveChatFile("chat", "file", choose); err == nil {
		t.Fatal("overwrote existing destination")
	}
	if err := a.saveChatFile("chat", "file", func(string) (string, error) { return "", nil }); err != nil {
		t.Fatal("cancel", err)
	}
}
