package transcribe

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type blobServer struct {
	data      []byte
	ranges    []string // Range headers seen
	ignore    bool     // answer 200 with everything, whatever the range
	cutAfter  int      // close the connection after this many bytes (0 = never), once
	hangAfter int      // send this many bytes, then stall, once
	hits      atomic.Int32
}

func (b *blobServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	n := b.hits.Add(1)
	b.ranges = append(b.ranges, r.Header.Get("Range"))
	if b.cutAfter > 0 && n == 1 {
		w.Header().Set("Content-Length", strconv.Itoa(len(b.data)))
		w.WriteHeader(http.StatusOK)
		w.Write(b.data[:b.cutAfter])
		panic(http.ErrAbortHandler) // drop the connection mid-body
	}
	if b.hangAfter > 0 && n == 1 {
		w.Header().Set("Content-Length", strconv.Itoa(len(b.data)))
		w.WriteHeader(http.StatusOK)
		w.Write(b.data[:b.hangAfter])
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		return
	}
	if b.ignore {
		w.Write(b.data)
		return
	}
	http.ServeContent(w, r, "blob", time.Time{}, bytes.NewReader(b.data))
}

func blob(t *testing.T, n int) ([]byte, string) {
	t.Helper()
	b := make([]byte, n)
	rand.Read(b)
	h := sha256.Sum256(b)
	return b, hex.EncodeToString(h[:])
}

func fetchFrom(t *testing.T, srv *httptest.Server, dst string, size int64, sum string) error {
	t.Helper()
	return fetchVerified(context.Background(), httpOpener(srv.Client(), srv.URL+"/f", "test"), dst, size, sum, nil)
}

func TestDownloadVerified(t *testing.T) {
	data, sum := blob(t, 300_000)
	b := &blobServer{data: data}
	srv := httptest.NewServer(b)
	defer srv.Close()
	dst := filepath.Join(t.TempDir(), "m.bin")
	var last int64
	err := fetchVerified(context.Background(), httpOpener(srv.Client(), srv.URL+"/f", "test"), dst, int64(len(data)), sum, func(d int64) { last = d })
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(dst); !bytes.Equal(got, data) {
		t.Fatal("content differs")
	}
	if _, err := os.Stat(dst + partialSuffix); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the partial file is left over")
	}
	if last != int64(len(data)) {
		t.Fatalf("progress ended at %d", last)
	}
}

func TestDownloadBadHashIsRefused(t *testing.T) {
	data, _ := blob(t, 50_000)
	_, other := blob(t, 10)
	srv := httptest.NewServer(&blobServer{data: data})
	defer srv.Close()
	dst := filepath.Join(t.TempDir(), "m.bin")
	err := fetchFrom(t, srv, dst, int64(len(data)), other)
	if !errors.Is(err, errChecksum) {
		t.Fatalf("err = %v", err)
	}
	for _, p := range []string{dst, dst + partialSuffix} {
		if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s exists after a checksum failure", filepath.Base(p))
		}
	}
	// A wrong size (the server sends more) is refused too.
	err = fetchFrom(t, srv, dst, int64(len(data))-10, other)
	if err == nil || !strings.Contains(err.Error(), "larger") {
		t.Fatalf("oversized: %v", err)
	}
	if _, err := os.Stat(dst); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("an oversized file was kept")
	}
}

func TestDownloadResumes(t *testing.T) {
	data, sum := blob(t, 200_000)
	dir := t.TempDir()
	dst := filepath.Join(dir, "m.bin")
	if err := os.WriteFile(dst+partialSuffix, data[:80_000], 0o600); err != nil {
		t.Fatal(err)
	}
	b := &blobServer{data: data}
	srv := httptest.NewServer(b)
	defer srv.Close()
	if err := fetchFrom(t, srv, dst, int64(len(data)), sum); err != nil {
		t.Fatal(err)
	}
	if len(b.ranges) != 1 || b.ranges[0] != "bytes=80000-" {
		t.Fatalf("ranges asked: %q", b.ranges)
	}
	if got, _ := os.ReadFile(dst); !bytes.Equal(got, data) {
		t.Fatal("content differs after resuming")
	}

	// A server that ignores the range: the download starts over and still checks out.
	os.Remove(dst)
	os.WriteFile(dst+partialSuffix, data[:1000], 0o600)
	srv2 := httptest.NewServer(&blobServer{data: data, ignore: true})
	defer srv2.Close()
	if err := fetchFrom(t, srv2, dst, int64(len(data)), sum); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(dst); !bytes.Equal(got, data) {
		t.Fatal("content differs after a restart")
	}

	// A partial file with wrong bytes fails the hash, is deleted, and the next try succeeds.
	os.Remove(dst)
	bad := append([]byte(nil), data[:5000]...)
	bad[10] ^= 0xFF
	os.WriteFile(dst+partialSuffix, bad, 0o600)
	if err := fetchFrom(t, srv, dst, int64(len(data)), sum); !errors.Is(err, errChecksum) {
		t.Fatalf("corrupt partial: %v", err)
	}
	if err := fetchFrom(t, srv, dst, int64(len(data)), sum); err != nil {
		t.Fatalf("after a corrupt partial: %v", err)
	}
}

func TestDownloadInterruptedThenResumed(t *testing.T) {
	data, sum := blob(t, 400_000)
	b := &blobServer{data: data, cutAfter: 150_000}
	srv := httptest.NewServer(b)
	defer srv.Close()
	dst := filepath.Join(t.TempDir(), "m.bin")
	err := fetchFrom(t, srv, dst, int64(len(data)), sum)
	if err == nil || !strings.Contains(err.Error(), "next try") {
		t.Fatalf("first try: %v", err)
	}
	if _, err := os.Stat(dst); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("an incomplete file got its final name")
	}
	st, err := os.Stat(dst + partialSuffix)
	if err != nil || st.Size() == 0 {
		t.Fatalf("no partial kept: %v", err)
	}
	if err := fetchFrom(t, srv, dst, int64(len(data)), sum); err != nil {
		t.Fatal(err)
	}
	if b.ranges[1] == "" {
		t.Fatal("the second try did not continue")
	}
}

func TestDownloadStall(t *testing.T) {
	old := stallTimeout
	stallTimeout = 300 * time.Millisecond
	defer func() { stallTimeout = old }()
	data, sum := blob(t, 100_000)
	srv := httptest.NewServer(&blobServer{data: data, hangAfter: 20_000})
	defer srv.Close()
	dst := filepath.Join(t.TempDir(), "m.bin")
	start := time.Now()
	err := fetchFrom(t, srv, dst, int64(len(data)), sum)
	if err == nil || !strings.Contains(err.Error(), "stalled") {
		t.Fatalf("err = %v", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("the stall was not cut off in time")
	}
	if err := fetchFrom(t, srv, dst, int64(len(data)), sum); err != nil {
		t.Fatalf("resume after a stall: %v", err)
	}
}

func TestRangeStart(t *testing.T) {
	for in, want := range map[string]int64{"bytes 100-199/200": 100, "bytes 0-9/*": 0} {
		if got, ok := rangeStart(in); !ok || got != want {
			t.Errorf("%q: %d %v", in, got, ok)
		}
	}
	if _, ok := rangeStart("items 1-2"); ok {
		t.Error("a bad header was accepted")
	}
}
