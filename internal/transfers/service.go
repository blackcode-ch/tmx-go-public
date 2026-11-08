package transfers

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/antechamber"
	"github.com/blackcode-ch/tmx-go/internal/config"
	"github.com/blackcode-ch/tmx-go/internal/sysinfo"
)

type Peer interface {
	Caller
	Known(string) bool
	Machine() string
	ExportStream(context.Context, string, string, int64) (*http.Response, error)
}

// Record uses Python's wire keys; timestamps are Unix seconds, IDs are 16 hex digits.
type Record map[string]any

func (r Record) str(k string) string { s, _ := r[k].(string); return s }
func (r Record) flag(k string) bool  { b, _ := r[k].(bool); return b }
func (r Record) size() int64 {
	switch n := r["size"].(type) {
	case int64:
		return n
	case float64:
		return int64(n)
	}
	return 0
}
func clone(r Record) Record {
	b, _ := json.Marshal(r)
	var out Record
	_ = json.Unmarshal(b, &out)
	return out
}
func now() float64  { return float64(time.Now().UnixNano()) / 1e9 }
func newID() string { b := make([]byte, 8); _, _ = rand.Read(b); return hex.EncodeToString(b) }

var idRx = regexp.MustCompile(`^[0-9a-f]{16}$`)
var shaRx = regexp.MustCompile(`^[0-9a-f]{64}$`)

// Windows does not permit replacing a file while os.ReadFile has it open. Serialize
// record reads/replacements in-process; short retries cover a separate CLI reader.
var recordMu sync.Mutex

type Error struct {
	Status  int
	Message string
}

func (e *Error) Error() string              { return e.Message }
func fail(status int, message string) error { return &Error{status, message} }

type Options struct {
	OwnerNames                                   []string
	Home, Install                                string
	Protected, SecretFolders, TrustedBackupRoots []string
	HoldNeedsHuman                               bool
	Peers                                        Peer
	Antechamber                                  *antechamber.Antechamber
	Logger                                       *slog.Logger
	// DiskSpace is injectable for deterministic reserve tests.
	DiskSpace func(string) (uint64, uint64, error)
}

type Service struct {
	Options
	Root     string
	Exports  *Exports
	ctx      context.Context
	cancel   context.CancelFunc
	mu       sync.Mutex
	placing  sync.Mutex
	jobs     map[string]context.CancelFunc
	finished map[string]chan struct{}
	wg       sync.WaitGroup
	slots    chan struct{}
}

func New(o Options) (*Service, error) {
	if o.Peers == nil {
		return nil, errors.New("transfers need fleet peers")
	}
	if o.Logger == nil {
		o.Logger = slog.New(slog.DiscardHandler)
	}
	if o.DiskSpace == nil {
		o.DiskSpace = sysinfo.DiskSpace
	}
	for i, p := range o.SecretFolders {
		p = expand(p)
		if abs, e := filepath.Abs(p); e == nil {
			p = abs
		}
		o.SecretFolders[i] = normalizePath(p)
	}
	if o.Antechamber == nil {
		if err := config.MkdirSecret(filepath.Join(o.Home, "antechamber")); err != nil {
			return nil, err
		}
		a, err := antechamber.New(filepath.Join(o.Home, "antechamber"), o.Peers.Machine())
		if err != nil {
			return nil, err
		}
		a.OwnerNames = append([]string(nil), o.OwnerNames...)
		o.Antechamber = a
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &Service{Options: o, Root: filepath.Join(o.Home, "transfers"), ctx: ctx, cancel: cancel, jobs: map[string]context.CancelFunc{}, finished: map[string]chan struct{}{}, slots: make(chan struct{}, 2)}
	if err := config.MkdirSecret(s.Root); err != nil {
		cancel()
		return nil, err
	}
	s.Exports = NewExports(filepath.Join(o.Home, "exports"))
	s.Exports.Home, s.Exports.Install = o.Home, o.Install
	s.Exports.Extra = append(append([]string{}, o.Protected...), o.SecretFolders...)
	return s, nil
}
func (s *Service) Close() {
	s.cancel()
	s.mu.Lock()
	for _, c := range s.jobs {
		c()
	}
	s.mu.Unlock()
	s.wg.Wait()
}
func (s *Service) save(r Record) error {
	recordMu.Lock()
	defer recordMu.Unlock()
	r["updated"] = now()
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	for attempt := 0; attempt < 10; attempt++ {
		err = config.WriteFileAtomic(filepath.Join(s.Root, r.str("id")+".json"), b, 0600)
		if !errors.Is(err, os.ErrPermission) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err == nil {
		s.Logger.Info("transfer", "id", r.str("id"), "state", r.str("state"))
	} else {
		s.Logger.Error("transfer record could not be saved", "id", r.str("id"), "error", err)
	}
	return err
}
func ReadRecord(root, id string) (Record, error) {
	recordMu.Lock()
	defer recordMu.Unlock()
	if !idRx.MatchString(id) {
		return nil, fail(404, "no such transfer")
	}
	b, err := os.ReadFile(filepath.Join(root, id+".json"))
	if err != nil {
		return nil, fail(404, "no such transfer")
	}
	var r Record
	if json.Unmarshal(b, &r) != nil || r.str("id") != id {
		return nil, fail(404, "no such transfer")
	}
	return r, nil
}
func ListRecords(root string) []Record {
	out := []Record{}
	entries, _ := os.ReadDir(root)
	for _, e := range entries {
		if r, err := ReadRecord(root, strings.TrimSuffix(e.Name(), ".json")); err == nil {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		a, _ := out[i]["updated"].(float64)
		b, _ := out[j]["updated"].(float64)
		return a > b
	})
	if len(out) > 200 {
		out = out[:200]
	}
	return out
}
func (s *Service) List() []Record { return ListRecords(s.Root) }
func done(st string) bool {
	return st == "placed" || st == "failed" || st == "cancelled" || st == "refused"
}
func (s *Service) launch(r Record, work func(context.Context, Record) error) {
	ctx, cancel := context.WithCancel(s.ctx)
	finished := make(chan struct{})
	s.mu.Lock()
	s.jobs[r.str("id")] = cancel
	s.finished[r.str("id")] = finished
	s.wg.Add(1)
	s.mu.Unlock()
	go func() {
		defer s.wg.Done()
		defer cancel()
		err := work(ctx, r)
		if err != nil && ctx.Err() == nil {
			r["state"], r["error"] = "failed", err.Error()
			_ = s.save(r)
			if r.str("dir") == "in" {
				_ = os.Remove(s.part(r))
				var response any
				_ = s.call(ctx, r.str("from"), "POST", "/api/exports/"+r.str("ticket")+"/done", Record{}, &response)
			}
		}
		s.mu.Lock()
		delete(s.jobs, r.str("id"))
		delete(s.finished, r.str("id"))
		close(finished)
		s.mu.Unlock()
	}()
}

// Start accepts the Python POST /api/transfers shape. The HTTP adapter authenticates peers.
func (s *Service) Start(ctx context.Context, b Record, by string) (Record, error) {
	if from := strings.ToLower(b.str("from")); from != "" && from != s.Peers.Machine() {
		forwarded := clone(b)
		delete(forwarded, "from")
		var out Record
		err := s.call(ctx, from, "POST", "/api/transfers", forwarded, &out)
		return out, err
	}
	to := strings.ToLower(b.str("to"))
	src := expand(b.str("src"))
	dest := b.str("dest")
	if b.flag("backup") {
		dest = "(backup)"
	}
	if b.flag("backup") && b.flag("trusted_backup") {
		return nil, fail(400, "choose backup or trusted_backup, not both")
	}
	if src == "" || to == "" || dest == "" || !filepath.IsAbs(src) {
		return nil, fail(400, "src (an absolute path), to and dest are required")
	}
	if to == s.Peers.Machine() {
		return nil, fail(400, "source and destination are the same machine")
	}
	if _, err := s.Route(ctx, to); err != nil {
		return nil, err
	}
	if err := s.sourceAllowed(src); err != nil {
		return nil, err
	}
	st, err := os.Stat(src)
	if err != nil {
		return nil, fail(404, "source does not exist")
	}
	kind, state := "file", "offering"
	if st.IsDir() {
		kind, state = "folder", "packing"
	} else if !st.Mode().IsRegular() {
		return nil, fail(400, "not a regular file or folder")
	}
	r := Record{"id": newID(), "dir": "out", "host": s.Peers.Machine(), "src": src, "name": filepath.Base(src), "kind": kind, "to": to, "dest": dest, "force": b.flag("force"), "backup": b.flag("backup"), "trusted_backup": b.flag("trusted_backup"), "by": by, "state": state, "created": now()}
	if err = s.save(r); err != nil {
		return nil, err
	}
	out := clone(r)
	s.launch(r, s.offer)
	return out, nil
}
func expand(p string) string {
	p = os.ExpandEnv(p)
	if p == "~" || strings.HasPrefix(p, "~/") || strings.HasPrefix(p, "~\\") {
		if h, e := os.UserHomeDir(); e == nil {
			p = filepath.Join(h, strings.TrimLeft(p[1:], "/\\"))
		}
	}
	// Python expands %VAR% on Windows too.
	p = regexp.MustCompile(`%([^%]+)%`).ReplaceAllStringFunc(p, func(v string) string {
		if x, ok := os.LookupEnv(v[1 : len(v)-1]); ok {
			return x
		}
		return v
	})
	return p
}
func (s *Service) sourceAllowed(p string) error {
	if err := noLinks(p); err != nil {
		return fail(403, err.Error())
	}
	for _, root := range s.SecretFolders {
		root = expand(root)
		if within(p, root) {
			return fail(403, "cannot send a secret folder")
		}
	}
	if why := ForbiddenSource(p, s.Home, s.Install, s.Protected...); why != "" {
		return fail(403, why)
	}
	return nil
}
func (s *Service) offer(ctx context.Context, r Record) error {
	var e Export
	var err error
	if r.str("kind") == "folder" {
		e, err = s.pack(ctx, r)
	} else {
		e, err = s.Exports.Prepare(r.str("src"), r.str("to"), r.str("dest"), r.str("id"), s.Home, s.Install)
	}
	if err != nil {
		return err
	}
	via, err := s.Route(ctx, e.To)
	if err != nil {
		s.Exports.Drop(e.Ticket)
		return err
	}
	if via != "" {
		e.Via = via
		if err = s.Exports.save(e); err != nil {
			s.Exports.Drop(e.Ticket)
			return err
		}
	}
	r["ticket"], r["size"], r["sha256"], r["state"] = e.Ticket, e.Size, e.SHA256, "offering"
	r["skipped"] = []string{}
	if err = s.save(r); err != nil {
		s.Exports.Drop(e.Ticket)
		return err
	}
	b := Record{"ticket": e.Ticket, "from": s.Peers.Machine(), "name": e.Name, "kind": e.Kind, "size": e.Size, "sha256": e.SHA256, "dest": e.Dest, "force": r.flag("force"), "out_id": r.str("id"), "backup": r.flag("backup"), "trusted_backup": r.flag("trusted_backup")}
	var res Record
	if err = s.call(ctx, e.To, "POST", "/api/transfers/incoming", b, &res); err != nil {
		s.Exports.Drop(e.Ticket)
		return err
	}
	if !idRx.MatchString(res.str("id")) {
		s.Exports.Drop(e.Ticket)
		return errors.New("target returned an invalid transfer id")
	}
	r["state"], r["remote_id"] = "offered", res.str("id")
	return s.save(r)
}
func (s *Service) destinationAllowed(dest, name string) error {
	if !filepath.IsAbs(dest) {
		return fail(400, "dest must be an absolute folder")
	}
	for _, p := range []string{dest, filepath.Join(dest, name)} {
		if err := noLinks(p); err != nil {
			return fail(403, err.Error())
		}
		roots := append(append([]string{}, s.Protected...), s.SecretFolders...)
		if why := ForbiddenDestination(p, s.Home, s.Install, roots...); why != "" {
			return fail(403, why)
		}
	}
	st, err := os.Stat(dest)
	if err != nil || !st.IsDir() {
		return fail(404, "dest is not an existing folder")
	}
	return nil
}
func exists(p string) bool { _, e := os.Lstat(p); return e == nil }
func (s *Service) room(path string, n int64) error {
	free, total, err := s.DiskSpace(path)
	if err != nil {
		return fail(507, "cannot check free disk space")
	}
	if n < 0 || free < uint64(n)+uint64(antechamber.ReserveFor(total)) {
		return fail(507, "not enough free space; disk reserve must remain free")
	}
	return nil
}
func (s *Service) Incoming(b Record, from string) (Record, error) {
	src := strings.ToLower(b.str("from"))
	size := b.size()
	sha := strings.ToLower(b.str("sha256"))
	kind := b.str("kind")
	if !ticketRx.MatchString(b.str("ticket")) || !shaRx.MatchString(sha) || (kind != "file" && kind != "folder") || b["size"] == nil || size < 0 {
		return nil, fail(400, "bad offer")
	}
	// No caller-chosen URL, identity, cloud or trust value can broaden a pair token's authority.
	if !config.ValidHostName(src) || !s.Peers.Known(from) {
		return nil, fail(403, "offer must come from an authenticated fleet machine")
	}
	if (b.flag("backup") || b.flag("trusted_backup")) && (src != from || !s.Peers.Known(src)) {
		return nil, fail(403, "backups must come directly from their fleet machine")
	}
	if size > MaxFile {
		return nil, fail(413, "copy exceeds 4 GB size cap")
	}
	if b.flag("backup") && b.flag("trusted_backup") {
		return nil, fail(400, "choose backup or trusted_backup, not both")
	}
	name, findings := antechamber.CleanName(b.str("name"))
	for _, f := range findings {
		if f.Level(antechamber.Own) == antechamber.Blocked {
			return nil, fail(403, "unsafe offered name")
		}
	}
	dest := expand(b.str("dest"))
	r := Record{"id": newID(), "dir": "in", "host": s.Peers.Machine(), "from": src, "via": nil, "ticket": b.str("ticket"), "name": name, "kind": kind, "size": size, "sha256": sha, "dest": dest, "force": b.flag("force"), "out_id": b["out_id"], "state": "queued", "received": 0, "created": now()}
	if !s.Peers.Known(src) {
		r["via"] = from
	}
	if b.flag("backup") || b.flag("trusted_backup") {
		if err := s.backupDestination(r, b); err != nil {
			return nil, err
		}
	} else {
		if err := s.destinationAllowed(dest, name); err != nil {
			return nil, err
		}
		if exists(filepath.Join(dest, name)) && !b.flag("force") {
			return nil, fail(409, "destination already exists; --force keeps the old copy aside")
		}
	}
	// Serialize admission and reserve for all unfinished receives, including queued jobs.
	s.mu.Lock()
	need := size * 2
	for _, old := range s.List() {
		if old.str("dir") == "in" && !done(old.str("state")) {
			need += old.size() * 2
		}
	}
	partDir := s.Root
	if r.flag("backup") {
		partDir = r.str("part_dir")
	}
	err := s.room(partDir, need)
	if err == nil && !r.flag("backup") {
		err = s.room(dest, need)
	}
	if err == nil {
		err = s.save(r)
	}
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	out := clone(r)
	s.launch(r, s.pull)
	return out, nil
}
func (s *Service) part(r Record) string {
	dir := s.Root
	if r.str("part_dir") != "" {
		dir = r.str("part_dir")
	}
	return filepath.Join(dir, r.str("id")+".part")
}
func (s *Service) pull(ctx context.Context, r Record) error {
	select {
	case s.slots <- struct{}{}:
		defer func() { <-s.slots }()
	case <-ctx.Done():
		return ctx.Err()
	}
	part := s.part(r)
	if r.str("state") != "inspecting" {
		if err := s.download(ctx, r, part); err != nil {
			return err
		}
		r["state"] = "verifying"
		if err := s.save(r); err != nil {
			return err
		}
		sum, err := hashFile(part)
		if err != nil {
			return err
		}
		if sum != r.str("sha256") {
			_ = os.Remove(part)
			return fail(409, "the copy arrived damaged (SHA-256 differs); nothing was placed")
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		r["state"] = "inspecting"
		if err = s.save(r); err != nil {
			return err
		}
	}
	var err error
	if r.flag("backup") {
		err = s.storeBackup(ctx, r, part)
	} else {
		err = s.inspectPlace(ctx, r, part)
	}
	if err == nil && done(r.str("state")) {
		_ = os.Remove(part)
		var response any
		_ = s.call(ctx, r.str("from"), "POST", "/api/exports/"+r.str("ticket")+"/done", Record{}, &response)
	}
	return err
}
func (s *Service) download(ctx context.Context, r Record, part string) error {
	r["state"] = "pulling"
	if err := s.save(r); err != nil {
		return err
	}
	if err := noLinks(part); err != nil {
		return err
	}
	start := time.Now()
	delay := time.Second
	for {
		offset := int64(0)
		if st, e := os.Stat(part); e == nil {
			offset = st.Size()
			if offset == r.size() {
				r["received"] = offset
				return nil
			}
		}
		if offset > r.size() {
			return errors.New("partial file exceeds offered size")
		}
		callCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
		res, err := s.exportStream(callCtx, r, offset)
		if err == nil {
			switch res.StatusCode {
			case 200:
				offset = 0
			case 206:
				if !strings.HasPrefix(res.Header.Get("Content-Range"), fmt.Sprintf("bytes %d-", offset)) {
					res.Body.Close()
					cancel()
					return errors.New("invalid resume Content-Range")
				}
			default:
				res.Body.Close()
				cancel()
				return fail(502, fmt.Sprintf("source refused export (HTTP %d)", res.StatusCode))
			}
			flags := os.O_WRONLY | os.O_CREATE
			if offset == 0 {
				flags |= os.O_TRUNC
			} else {
				flags |= os.O_APPEND
			}
			f, e := os.OpenFile(part, flags, 0600)
			if e != nil {
				res.Body.Close()
				cancel()
				return e
			}
			guarded := &reserveWriter{writer: f, check: func() error { return s.room(filepath.Dir(part), 0) }}
			n, e := io.Copy(guarded, io.LimitReader(res.Body, r.size()-offset+1))
			closeErr := f.Close()
			res.Body.Close()
			cancel()
			if closeErr != nil {
				return closeErr
			}
			var reserveErr *Error
			if errors.As(e, &reserveErr) && reserveErr.Status == 507 {
				return e
			}
			if n+offset > r.size() {
				_ = os.Remove(part)
				return fail(409, "received more bytes than offered")
			}
			r["received"] = n + offset
			_ = s.save(r)
			if e == nil && n+offset == r.size() {
				return nil
			}
			err = e
			if e == nil {
				return fail(409, "received size differs from offer")
			}
		} else {
			cancel()
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if time.Since(start) > 30*time.Minute {
			return fmt.Errorf("download failed: %w", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
		delay = min(delay*2, time.Minute)
	}
}

// Recheck the reserve while receiving as other work can consume the same disk.
type reserveWriter struct {
	writer  io.Writer
	check   func() error
	written int64
}

func (w *reserveWriter) Write(p []byte) (int, error) {
	if w.written == 0 || w.written/(64<<20) != (w.written+int64(len(p)))/(64<<20) {
		if err := w.check(); err != nil {
			return 0, err
		}
	}
	n, err := w.writer.Write(p)
	w.written += int64(n)
	return n, err
}
func (s *Service) inspectPlace(ctx context.Context, r Record, part string) error {
	name := r.str("name")
	if r.str("kind") == "folder" {
		name += ".zip"
	}
	a := s.Antechamber
	var it antechamber.Item
	var err error
	if r.str("item") != "" {
		it, err = a.Get(r.str("item"))
	} else {
		it, err = a.AdmitPath(part, name, antechamber.Origin{Kind: "transfer", Trust: antechamber.Own, From: r.str("from"), Dest: r.str("dest")}, r.str("kind") == "folder", false, r.str("sha256"))
	}
	if err != nil {
		return err
	}
	r["item"], r["verdict"] = it.ID, it.Verdict
	if err = s.save(r); err != nil {
		return err
	}
	_ = os.Remove(part)
	// A crash after placement but before the transfer record was saved must not
	// replace the already-placed copy again.
	if it.State == antechamber.StateReleased {
		r["state"], r["placed"], r["replaced_to"], r["error"] = "placed", it.ReleasedTo, it.ReplacedTo, nil
		return s.save(r)
	}
	reasons := []string{}
	for _, reason := range it.Reasons {
		if reason.Level != antechamber.Clean {
			reasons = append(reasons, reason.Text)
		}
	}
	r["reasons"] = reasons
	if updated, label, ok, e := a.AutoRelease(it.ID, r.str("from"), !s.HoldNeedsHuman); e != nil {
		return e
	} else if ok {
		it = updated
		r["auto_released"], r["verdict"], r["inspected_verdict"], r["label"] = true, "auto-released", antechamber.NeedsHuman, label
	}
	if !antechamber.Releasable(it) {
		r["state"] = "waiting_approval"
		if it.Verdict == antechamber.Blocked {
			r["state"], r["error"] = "refused", "blocked by the antechamber"
		}
		return s.save(r)
	}
	s.placing.Lock()
	defer s.placing.Unlock()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err = s.destinationAllowed(r.str("dest"), r.str("name")); err != nil {
		return err
	}
	if exists(filepath.Join(r.str("dest"), r.str("name"))) && !r.flag("force") {
		return fail(409, "destination already exists; nothing placed")
	}
	if err = s.room(r.str("dest"), r.size()); err != nil {
		return err
	}
	backup := ""
	if r.flag("force") {
		backup = filepath.Join(s.Root, "replaced", r.str("id"))
		if err = os.MkdirAll(backup, 0700); err != nil {
			return err
		}
	}
	placed, err := a.Release(it.ID, r.str("dest"), antechamber.ReleaseOptions{By: "transfer from " + r.str("from"), Name: r.str("name"), ReplaceInto: backup})
	if err != nil {
		return err
	}
	it, err = a.Get(it.ID)
	if err != nil {
		return err
	}
	r["state"], r["placed"], r["replaced_to"], r["error"] = "placed", placed, it.ReplacedTo, nil
	return s.save(r)
}
func (s *Service) Status(ctx context.Context, id string) (Record, error) {
	r, err := ReadRecord(s.Root, id)
	if err != nil {
		return nil, err
	}
	if r.str("dir") == "out" && r.str("state") == "offered" {
		var far Record
		if e := s.call(ctx, r.str("to"), "GET", "/api/transfers/"+r.str("remote_id"), nil, &far); e == nil {
			status := Record{}
			for _, k := range []string{"state", "received", "placed", "error", "reasons", "replaced_to", "item", "label", "auto_released", "inspection", "report_url"} {
				status[k] = far[k]
			}
			r["far"] = status
			if done(far.str("state")) {
				r["state"], r["error"] = far["state"], far["error"]
				s.Exports.Drop(r.str("ticket"))
			}
			_ = s.save(r)
		} else {
			r["far_error"] = e.Error()
		}
	}
	return r, nil
}

// Resume schedules durable incoming records after restart. Waiting decisions stay held.
func (s *Service) Resume() {
	for _, r := range s.List() {
		if r.str("dir") == "in" && (r.str("state") == "queued" || r.str("state") == "pulling" || r.str("state") == "verifying" || r.str("state") == "inspecting") {
			s.launch(r, s.pull)
		}
	}
}

func (s *Service) Cancel(ctx context.Context, id, by string) (Record, error) {
	r, err := ReadRecord(s.Root, id)
	if err != nil || done(r.str("state")) {
		return r, err
	}
	s.mu.Lock()
	cancel := s.jobs[id]
	finished := s.finished[id]
	if cancel != nil {
		cancel()
	}
	s.mu.Unlock()
	if finished != nil {
		select {
		case <-finished:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	r, err = ReadRecord(s.Root, id)
	if err != nil || done(r.str("state")) {
		return r, err
	}
	if r.str("dir") == "in" {
		_ = os.Remove(s.part(r))
		if r.str("item") != "" {
			_, _ = s.Antechamber.Refuse(r.str("item"), by, false)
		}
	} else {
		s.Exports.Drop(r.str("ticket"))
		if r.str("remote_id") != "" {
			var out any
			_ = s.call(ctx, r.str("to"), "POST", "/api/transfers/"+r.str("remote_id")+"/cancel", Record{}, &out)
		}
	}
	r["state"], r["error"] = "cancelled", "cancelled by "+by
	return r, s.save(r)
}
