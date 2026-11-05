package transcribe

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/sysinfo"
)

// Downloads are resumable and verified. A file is written to <name>.partial; an interrupted
// download continues from there (a Range request), and only once the whole file has the size
// and SHA-256 it must have is it renamed to its name. Nothing ever uses a .partial file, so a
// half-downloaded or forged file is never run or loaded. A partial file whose hash turns out
// wrong is deleted, so the next try starts over.

// opener starts the download at byte offset: an answer of 206 continues there, 200 is the
// whole file (the server ignored the range).
type opener func(ctx context.Context, offset int64) (*http.Response, error)

// stallTimeout: a download that receives nothing for this long is cut off (it resumes on the
// next try).
var stallTimeout = 60 * time.Second

const partialSuffix = ".partial"

// errChecksum: the downloaded file is not the pinned one.
var errChecksum = errors.New("does not match its checksum")

// fetchVerified downloads to dst (through dst.partial) a file of size bytes whose SHA-256 is sum
// (lowercase hex). progress gets the bytes done so far.
func fetchVerified(ctx context.Context, open opener, dst string, size int64, sum string, progress func(done int64)) error {
	if size <= 0 || len(sum) != 64 {
		return fmt.Errorf("%s: no size or checksum to check it against", filepath.Base(dst))
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	part := dst + partialSuffix
	f, err := os.OpenFile(part, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		f.Close()
		if !ok {
			// Keep a partial file that can be continued; drop one that cannot.
			if st, err := os.Stat(part); err == nil && st.Size() >= size {
				os.Remove(part)
			}
		}
	}()

	// What is there already counts towards the hash.
	h := sha256.New()
	have, err := io.Copy(h, io.LimitReader(f, size+1))
	if err != nil {
		return err
	}
	if have > size {
		if err := restart(f, &have); err != nil {
			return err
		}
		h.Reset()
	}
	if free, _, err := sysinfo.DiskSpace(filepath.Dir(dst)); err == nil && free < uint64(size-have)+64<<20 {
		return fmt.Errorf("not enough disk space for %s (%s needed, %s free)", filepath.Base(dst), mb(size-have), mb(int64(free)))
	}
	if progress != nil {
		progress(have)
	}

	if have < size {
		ctx, cancel := context.WithCancel(ctx)
		defer cancel()
		resp, err := open(ctx, have)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		switch resp.StatusCode {
		case http.StatusPartialContent:
			if start, ok := rangeStart(resp.Header.Get("Content-Range")); !ok || start != have {
				return fmt.Errorf("%s: the server continued at the wrong place (%q)", filepath.Base(dst), resp.Header.Get("Content-Range"))
			}
		case http.StatusOK:
			if have > 0 { // the server ignored the range: start over
				if err := restart(f, &have); err != nil {
					return err
				}
				h.Reset()
			}
		default:
			return fmt.Errorf("%s: the server answered %s", filepath.Base(dst), resp.Status)
		}
		if _, err := f.Seek(have, io.SeekStart); err != nil {
			return err
		}
		body := &watchdog{r: resp.Body, cancel: cancel}
		defer body.stop()
		w := &progressWriter{w: io.MultiWriter(f, h), done: have, report: progress}
		n, err := io.Copy(w, io.LimitReader(body, size-have+1))
		have += n
		if body.stalled() {
			return fmt.Errorf("%s: the download stalled (it continues on the next try)", filepath.Base(dst))
		}
		if err != nil {
			if ctx.Err() != nil {
				return context.Canceled
			}
			return fmt.Errorf("downloading %s: %w (it continues on the next try)", filepath.Base(dst), err)
		}
		if have > size {
			return fmt.Errorf("%s is larger than it should be (%d bytes)", filepath.Base(dst), size)
		}
		if have < size {
			return fmt.Errorf("%s: the download ended early (%s of %s; it continues on the next try)", filepath.Base(dst), mb(have), mb(size))
		}
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != sum {
		os.Remove(part)
		return fmt.Errorf("%s %w (got %s…); the download was deleted", filepath.Base(dst), errChecksum, got[:12])
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(part, dst); err != nil {
		return err
	}
	ok = true
	return nil
}

// restart empties the partial file.
func restart(f *os.File, have *int64) error {
	if err := f.Truncate(0); err != nil {
		return err
	}
	*have = 0
	_, err := f.Seek(0, io.SeekStart)
	return err
}

// rangeStart reads the first byte of "bytes 100-199/200".
func rangeStart(cr string) (int64, bool) {
	rest, ok := strings.CutPrefix(cr, "bytes ")
	if !ok {
		return 0, false
	}
	first, _, ok := strings.Cut(rest, "-")
	if !ok {
		return 0, false
	}
	n, err := strconv.ParseInt(first, 10, 64)
	return n, err == nil
}

type progressWriter struct {
	w      io.Writer
	done   int64
	report func(int64)
}

func (p *progressWriter) Write(b []byte) (int, error) {
	n, err := p.w.Write(b)
	p.done += int64(n)
	if p.report != nil {
		p.report(p.done)
	}
	return n, err
}

// watchdog cancels the download when no byte arrives for stallTimeout.
type watchdog struct {
	r      io.Reader
	cancel context.CancelFunc
	timer  *time.Timer
	fired  chan struct{}
	once   sync.Once
}

func (w *watchdog) Read(p []byte) (int, error) {
	if w.timer == nil {
		w.fired = make(chan struct{})
		w.timer = time.AfterFunc(stallTimeout, func() { w.once.Do(func() { close(w.fired) }); w.cancel() })
	}
	n, err := w.r.Read(p)
	if n > 0 {
		w.timer.Reset(stallTimeout)
	}
	return n, err
}

func (w *watchdog) stop() {
	if w.timer != nil {
		w.timer.Stop()
	}
}

func (w *watchdog) stalled() bool {
	if w.fired == nil {
		return false
	}
	select {
	case <-w.fired:
		return true
	default:
		return false
	}
}

// httpOpener downloads url with a plain GET (Hugging Face; no token).
func httpOpener(c *http.Client, url, userAgent string) opener {
	return func(ctx context.Context, offset int64) (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", userAgent)
		if offset > 0 {
			req.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
		}
		return c.Do(req)
	}
}

func mb(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.0f MB", float64(n)/(1<<20))
	default:
		return fmt.Sprintf("%d KB", (n+1023)/1024)
	}
}
