// Package transfers copies files between the owner's machines. This part speaks the Python
// tmx's pull protocol, so Go and Python machines can copy in either direction: the source
// offers an export ticket, the target pulls the bytes itself (resumable, HTTP Range), checks
// size and SHA-256, and holds the copy in its antechamber. See docs/transfers.md.
//
// The source side (Python transfers.Transfers._offer / exported / export_done):
//   - Offer records exports/<ticket>.json (24 h) and POSTs /api/transfers/incoming to the target
//     with {ticket, from, name, kind, size, sha256, dest, force, out_id};
//   - the target GETs /api/exports/<ticket> (Range) and POSTs /api/exports/<ticket>/done;
//   - a ticket is served only to the machine it was offered to.
package transfers

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/config"
)

const (
	// ExportTTL is how long a ticket waits for its target (Python EXPORT_TTL_S).
	ExportTTL = 24 * time.Hour
	// MaxFile is the largest file a copy takes (Python max_file_own).
	MaxFile = 4 << 30
)

// Errors.
var (
	ErrNoExport  = errors.New("no such export (it expired or was fetched)")
	ErrNotYours  = errors.New("this export was offered to another machine")
	ErrForbidden = errors.New("this place may not be copied from")
)

// Export is one offered file.
type Export struct {
	Temp    bool      `json:"temp"`
	Via     string    `json:"via,omitempty"` // the configured one-hop relay, when the target is not direct
	Ticket  string    `json:"ticket"`
	Path    string    `json:"path"` // the file on this machine
	Name    string    `json:"name"`
	Kind    string    `json:"kind"` // "file" or "folder" (stored ZIP)
	Size    int64     `json:"size"`
	SHA256  string    `json:"sha256"`
	ModTime time.Time `json:"mod_time"` // the file as offered: a changed file is not served
	To      string    `json:"to"`       // the machine it is offered to
	Dest    string    `json:"dest"`
	ID      string    `json:"id"` // this side's transfer id (out_id)
	Created time.Time `json:"created"`
}

// Exports keeps the offered tickets in dir (a secret folder: a ticket lets its holder read the file).
type Exports struct {
	dir string
	mu  sync.Mutex
	now func() time.Time
	// Home, Install and Extra are never sources: checked when a file is offered and again when
	// it is served (it may have been swapped meanwhile).
	Home, Install string
	Extra         []string
}

// NewExports keeps tickets in dir.
func NewExports(dir string) *Exports { return &Exports{dir: dir, now: time.Now} }

var ticketRx = regexp.MustCompile(`^[A-Za-z0-9_-]{16,64}$`)

func (x *Exports) file(ticket string) (string, error) {
	if !ticketRx.MatchString(ticket) {
		return "", ErrNoExport
	}
	return filepath.Join(x.dir, ticket+".json"), nil
}

// Prepare checks src and records a ticket for it, offered to machine to (nothing is sent: see
// Offer). home and install are tmx's state and installation folders (never sources).
func (x *Exports) Prepare(src, to, dest, id, home, install string) (Export, error) {
	x.Purge()
	p, err := filepath.Abs(src)
	if err != nil {
		return Export{}, err
	}
	// The path as it really is: a link in it is followed once, here, never again when served.
	p = normalizePath(p)
	if why := ForbiddenSource(p, home, install, x.Extra...); why != "" {
		return Export{}, fmt.Errorf("%w: %s", ErrForbidden, why)
	}
	st, err := os.Lstat(p)
	if err != nil {
		return Export{}, err
	}
	if !st.Mode().IsRegular() {
		return Export{}, errors.New("a direct export must be a regular file; folders must be packed first")
	}
	if st.Size() > MaxFile {
		return Export{}, fmt.Errorf("the file is larger than %d GB", MaxFile>>30)
	}
	sum, err := hashFile(p)
	if err != nil {
		return Export{}, err
	}
	t, err := newTicket()
	if err != nil {
		return Export{}, err
	}
	e := Export{Ticket: t, Path: p, Name: filepath.Base(src), Kind: "file", Size: st.Size(), SHA256: sum,
		ModTime: st.ModTime(), To: to, Dest: dest, ID: id, Created: x.now()}
	return e, x.save(e)
}

func (x *Exports) save(e Export) error {
	x.mu.Lock()
	defer x.mu.Unlock()
	if err := config.MkdirSecret(x.dir); err != nil {
		return err
	}
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	f, _ := x.file(e.Ticket)
	return config.WriteSecretFile(f, b)
}

// Get is the export behind ticket, for machine from: only the machine it was offered to, only
// before it expired, only while the file is unchanged.
func (x *Exports) Get(ticket, from string) (Export, error) {
	f, err := x.file(ticket)
	if err != nil {
		return Export{}, err
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	b, err := os.ReadFile(f)
	if err != nil {
		return Export{}, ErrNoExport
	}
	var e Export
	if json.Unmarshal(b, &e) != nil || e.Ticket != ticket {
		return Export{}, ErrNoExport
	}
	if x.now().Sub(e.Created) > ExportTTL {
		_ = os.Remove(f)
		return Export{}, ErrNoExport
	}
	if e.To != from && (e.Via == "" || e.Via != from) {
		return Export{}, ErrNotYours
	}
	st, err := os.Lstat(e.Path)
	if err != nil || !st.Mode().IsRegular() || st.Size() != e.Size || !st.ModTime().Equal(e.ModTime) {
		return Export{}, errors.New("the file changed since it was offered")
	}
	// Re-pointed meanwhile (a parent folder made a link)? Its real place must still be the
	// one offered, and still not a forbidden one (o5, #259).
	if err := noLinks(e.Path); err != nil {
		return Export{}, errors.New("the file's path changed since it was offered")
	}
	e.Path = normalizePath(e.Path)
	if why := ForbiddenSource(e.Path, x.Home, x.Install, x.Extra...); why != "" && !x.ownedPack(e) {
		return Export{}, fmt.Errorf("%w: %s", ErrForbidden, why)
	}
	return e, nil
}

// Done drops the ticket once its target fetched it (only that machine may).
func (x *Exports) Done(ticket, from string) error {
	f, err := x.file(ticket)
	if err != nil {
		return err
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	b, err := os.ReadFile(f)
	if err != nil {
		return nil // already gone
	}
	var e Export
	if json.Unmarshal(b, &e) == nil && e.To != from && (e.Via == "" || e.Via != from) {
		return ErrNotYours
	}
	if x.ownedPack(e) {
		_ = os.Remove(e.Path)
	}
	return os.Remove(f)
}

// Drop removes a ticket whatever its state (the offer failed, or the owner cancelled).
func (x *Exports) Drop(ticket string) {
	if f, err := x.file(ticket); err == nil {
		x.mu.Lock()
		var e Export
		if b, err := os.ReadFile(f); err == nil && json.Unmarshal(b, &e) == nil && x.ownedPack(e) {
			_ = os.Remove(e.Path)
		}
		_ = os.Remove(f)
		x.mu.Unlock()
	}
}

// Purge removes expired tickets.
func (x *Exports) Purge() {
	x.mu.Lock()
	defer x.mu.Unlock()
	ents, _ := os.ReadDir(x.dir)
	for _, d := range ents {
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".json") || !ticketRx.MatchString(strings.TrimSuffix(d.Name(), ".json")) {
			continue
		}
		f := filepath.Join(x.dir, d.Name())
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var e Export
		if json.Unmarshal(b, &e) != nil || x.now().Sub(e.Created) > ExportTTL {
			if x.ownedPack(e) {
				_ = os.Remove(e.Path)
			}
			_ = os.Remove(f)
		}
	}
}

func (x *Exports) ownedPack(e Export) bool {
	return e.Temp && ticketRx.MatchString(e.Ticket) && noLinks(e.Path) == nil && samePath(e.Path, filepath.Join(x.dir, e.Ticket+".zip"))
}

// Caller is how the source reaches a peer machine (peers.Client.Call).
type Caller interface {
	Call(ctx context.Context, host, method, path string, body, out any) error
}

// Offer tells machine e.To that e waits for it (Python's /api/transfers/incoming); from is this
// machine's name, force whether the target may replace an existing file (it moves it aside).
func Offer(ctx context.Context, c Caller, e Export, from string, force bool) (map[string]any, error) {
	body := map[string]any{"ticket": e.Ticket, "from": from, "name": e.Name, "kind": e.Kind, "size": e.Size,
		"sha256": e.SHA256, "dest": e.Dest, "force": force, "out_id": e.ID}
	var res map[string]any
	if err := c.Call(ctx, e.To, "POST", "/api/transfers/incoming", body, &res); err != nil {
		return nil, err
	}
	return res, nil
}

func hashFile(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// newTicket is Python's secrets.token_urlsafe(24).
func newTicket() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
