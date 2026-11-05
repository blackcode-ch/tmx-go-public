package jobs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

const ChunkSize = 256 << 10

var (
	ErrBusy        = errors.New("job capacity or persistence busy; retry with the same identity")
	ErrForbidden   = errors.New("job belongs to another principal")
	ErrQuarantined = errors.New("job quarantined; explicit release required")
	ErrDegraded    = errors.New("governor degraded; background work denied")
	ErrDeferred    = errors.New("temporary source failure; deferred")
	ErrDisabled    = errors.New("job workers disabled; recovery requires inspection")
	ErrPersistence = errors.New("job persistence unavailable")
)

// Identity is supplied by the trusted adapter, never copied from an HTTP body.
// Caller and Owner are authenticated stable principals; Key names the operation.
type Identity struct{ Caller, Owner, Key string }

func (i Identity) ID() string {
	b, _ := json.Marshal(i)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

type Record struct {
	Peer        string    `json:"peer,omitempty"`
	Chat        string    `json:"chat,omitempty"`
	ID          string    `json:"id"`
	Identity    Identity  `json:"identity"`
	Foreground  bool      `json:"foreground"`
	State       string    `json:"state"`
	Phase       string    `json:"phase"`
	Bytes       int64     `json:"bytes"`
	Total       int64     `json:"total"`
	Heartbeat   time.Time `json:"heartbeat"`
	Failures    int       `json:"failures"`
	Fingerprint string    `json:"fingerprint,omitempty"`
	Digest      string    `json:"digest,omitempty"`
	Reason      string    `json:"reason,omitempty"`
	// Card is durable and visible in the owner job view, including after restart.
	Card bool `json:"card"`
}
type entry struct {
	record Record
	cancel context.CancelFunc
}
type work struct {
	id   string
	ctx  context.Context
	task AuditTask
}

// AuditTask belongs to a trusted adapter. Commit runs only after exact length and
// optional digest verification, with the input closed; Cleanup always runs in the
// worker. Neither callback may grant content execution or bypass authorization.
type AuditTask struct {
	Peer           string // trusted chat adapter's separate admission budget
	Chat           string
	Open           func(context.Context) (io.ReadCloser, error)
	ExpectedDigest string
	Commit         func(context.Context) error
	Cleanup        func()
}
type Options struct {
	Path     string
	MaxJobs  int
	Failures int
	// Fresh is a nonblocking cached observation (never a governor RPC or sampler).
	Fresh func() bool
	// BytesPerSecond bounds the single bulk worker even during governor failure.
	BytesPerSecond int64
}
type Manager struct {
	workers      atomic.Int32
	workerFailed atomic.Bool
	dirty        chan struct{}
	disabled     bool
	foreground   chan work
	releases     chan release
	o            Options
	mu           sync.Mutex // cached records only; never held during I/O
	entries      map[string]*entry
	mutation     sync.Mutex // durable transactions; control reads/cancel never acquire it
	queue        chan work
	ctx          context.Context
	stop         context.CancelFunc
	done         chan struct{}
	persistence  atomic.Bool
}

type release struct {
	id, principal string
	owner         bool
}

// RequestRelease queues a bounded persistence operation. The returned record is
// still quarantined until the durable release succeeds; status is authoritative.
func (m *Manager) RequestRelease(id, principal string, owner bool) (Record, error) {
	r, err := m.Get(id, principal, owner)
	if err != nil {
		return r, err
	}
	if r.State == "released" {
		return r, nil
	}
	if r.State != "quarantined" {
		return r, errors.New("job is not quarantined")
	}
	select {
	case m.releases <- release{id, principal, owner}:
		return r, nil
	default:
		return r, ErrBusy
	}
}

func (m *Manager) releaseWorker() {
	defer m.workerDone()
	defer m.containPanic()
	for {
		select {
		case <-m.ctx.Done():
			return
		case <-m.dirty:
			m.mutation.Lock()
			m.persist()
			m.mutation.Unlock()
		case r := <-m.releases:
			for {
				_, err := m.Release(r.id, r.principal, r.owner)
				if !errors.Is(err, ErrBusy) {
					break
				}
				t := time.NewTimer(10 * time.Millisecond)
				select {
				case <-m.ctx.Done():
					t.Stop()
					return
				case <-t.C:
				}
			}
		}
	}
}

// New recovers metadata before serving requests. It never resumes recovered work.
// Interrupted queued/running jobs are quarantined even on the first interruption.
func New(o Options) (*Manager, error) {
	if o.Path == "" {
		return nil, errors.New("job persistence path required")
	}
	if o.MaxJobs <= 0 {
		o.MaxJobs = 1000
	}
	if o.MaxJobs > 1000 {
		return nil, errors.New("job cap exceeds 1000")
	}
	if o.Failures <= 0 {
		o.Failures = 3
	}
	if o.BytesPerSecond <= 0 {
		o.BytesPerSecond = 20 << 20
	}
	cx, stop := context.WithCancel(context.Background())
	m := &Manager{o: o, entries: map[string]*entry{}, queue: make(chan work, o.MaxJobs), ctx: cx, stop: stop, done: make(chan struct{})}
	m.foreground = make(chan work, o.MaxJobs)
	m.dirty = make(chan struct{}, 1)
	m.releases = make(chan release, 16)
	f, err := os.Open(o.Path)
	if err == nil {
		defer f.Close()
		st, se := f.Stat()
		if se != nil || st.Size() > 8<<20 {
			stop()
			return nil, errors.New("job ledger size unavailable or excessive")
		}
		var rows []Record
		// JSON escaping can expand each bounded identity byte up to six bytes.
		// Keep recovery large enough for the full 1,000-record admitted ledger.
		d := json.NewDecoder(io.LimitReader(f, 8<<20))
		if err = d.Decode(&rows); err != nil {
			stop()
			return nil, err
		}
		var extra any
		if err = d.Decode(&extra); err != io.EOF {
			stop()
			return nil, errors.New("job ledger has trailing data")
		}
		if len(rows) > o.MaxJobs {
			stop()
			return nil, ErrBusy
		}
		for _, r := range rows {
			if r.ID != r.Identity.ID() {
				stop()
				return nil, errors.New("job identity mismatch")
			}
			if r.State == "queued" || r.State == "running" || r.State == "cancelling" {
				r.State = "quarantined"
				r.Reason = "interrupted by restart; inspect before release"
				r.Failures++
				r.Card = r.Foreground
				r.Fingerprint = fingerprint(r, "interrupted")
			}
			m.entries[r.ID] = &entry{record: r}
		}
		if err = f.Close(); err != nil {
			stop()
			return nil, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		stop()
		return nil, err
	}
	if err = m.persist(); err != nil {
		stop()
		return nil, err
	}
	m.workers.Store(2)
	go m.run()
	go m.releaseWorker()
	return m, nil
}

// Heartbeat records real worker progress, never an independent timer that could
// disguise a blocked input. A stale heartbeat with a live control plane is visible.

func fingerprint(r Record, reason string) string {
	h := sha256.Sum256([]byte(r.ID + "\x00" + reason))
	return hex.EncodeToString(h[:])
}
func (m *Manager) Degraded() bool           { return m.o.Fresh == nil || !m.o.Fresh() }
func (m *Manager) PersistenceHealthy() bool { return m.persistence.Load() }
func (m *Manager) Disabled() bool           { return m.disabled || m.workerFailed.Load() }
func (m *Manager) records() []Record {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Record, 0, len(m.entries))
	for _, e := range m.entries {
		out = append(out, e.record)
	}
	return out
}
func (m *Manager) List(principal string, owner bool) []Record {
	out := []Record{}
	for _, r := range m.records() {
		if owner || r.Identity.Owner == principal {
			out = append(out, r)
		}
	}
	return out
}
func (m *Manager) Get(id, principal string, owner bool) (Record, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e := m.entries[id]
	if e == nil {
		return Record{}, os.ErrNotExist
	}
	if !owner && e.record.Identity.Owner != principal {
		return Record{}, ErrForbidden
	}
	return e.record, nil
}

// SubmitAudit is for an already-authorized transfer adapter. No filesystem path
// is accepted from a public API. open is called by the worker, after admission.
// Retry the same identity on timeout/busy; accepted identities are idempotent.
func (m *Manager) SubmitAudit(i Identity, foreground bool, total int64, open func(context.Context) (io.ReadCloser, error)) (Record, error) {
	return m.SubmitTask(i, foreground, total, AuditTask{Open: open})
}

func (m *Manager) SubmitTask(i Identity, foreground bool, total int64, task AuditTask) (Record, error) {
	if m.Disabled() {
		return Record{}, ErrDisabled
	}
	if foreground && (i.Caller != "owner" || i.Owner != "owner") {
		return Record{}, ErrForbidden
	}
	if task.Peer != "" {
		if foreground || i.Owner != "owner" || i.Caller != "chat-peer:"+task.Peer || task.Chat == "" || len(task.Peer) > 128 || len(task.Chat) > 128 {
			return Record{}, ErrForbidden
		}
	} else if i.Owner != i.Caller {
		return Record{}, ErrForbidden
	}
	open := task.Open
	if i.Caller == "" || i.Owner == "" || i.Key == "" || len(i.Caller)+len(i.Owner)+len(i.Key) > 1024 || open == nil || total < 0 {
		return Record{}, errors.New("invalid job")
	}
	if task.ExpectedDigest != "" {
		b, err := hex.DecodeString(task.ExpectedDigest)
		if err != nil || len(b) != sha256.Size {
			return Record{}, errors.New("invalid expected digest")
		}
	}
	if !m.mutation.TryLock() {
		return Record{}, ErrBusy
	}
	defer m.mutation.Unlock()
	if m.ctx.Err() != nil {
		return Record{}, m.ctx.Err()
	}
	id := i.ID()
	m.mu.Lock()
	previous := m.entries[id]
	if previous != nil && previous.record.State != "failed" && previous.record.State != "released" && previous.record.State != "paused" {
		r := previous.record
		m.mu.Unlock()
		if r.State == "quarantined" {
			return r, ErrQuarantined
		}
		return r, nil
	}
	if !foreground && m.Degraded() {
		m.mu.Unlock()
		return Record{}, ErrDegraded
	}
	if !m.capacityLocked(i, foreground, task, previous == nil) {
		m.mu.Unlock()
		return Record{}, ErrBusy
	}
	cx, cancel := context.WithCancel(m.ctx)
	r := Record{ID: id, Identity: i, Foreground: foreground, Peer: task.Peer, Chat: task.Chat, State: "queued", Phase: "waiting for worker", Total: total, Heartbeat: time.Now()}
	if previous != nil {
		r.Failures = previous.record.Failures
	}
	m.entries[id] = &entry{record: r, cancel: cancel}
	m.mu.Unlock()
	if err := m.persist(); err != nil {
		cancel()
		m.mu.Lock()
		if previous == nil {
			delete(m.entries, id)
		} else {
			m.entries[id] = previous
		}
		m.mu.Unlock()
		return Record{}, err
	}
	w := work{id: id, ctx: cx, task: task}
	if foreground {
		m.foreground <- w
	} else {
		m.queue <- w
	}
	return r, nil
}

// Wait is for background adapters only; HTTP status and cancel use cached Get.
// Cancelling this wait also cancels the job so the adapter cannot leave an orphan.
func (m *Manager) Wait(ctx context.Context, id, principal string, owner bool) (Record, error) {
	t := time.NewTicker(10 * time.Millisecond)
	defer t.Stop()
	for {
		r, err := m.Get(id, principal, owner)
		if err != nil {
			return r, err
		}
		switch r.State {
		case "completed", "failed", "cancelled", "quarantined", "paused", "revoked":
			return r, nil
		}
		select {
		case <-ctx.Done():
			m.Cancel(id, principal, owner)
			return r, ctx.Err()
		case <-m.ctx.Done():
			return r, m.ctx.Err()
		case <-t.C:
		}
	}
}

// Cancel is independent of persistence and the governor. A blocked OS read remains
// cancelling until it returns; it retains the worker reservation during that time.
func (m *Manager) Cancel(id, principal string, owner bool) (Record, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e := m.entries[id]
	if e == nil {
		return Record{}, os.ErrNotExist
	}
	if !owner && e.record.Identity.Owner != principal {
		return Record{}, ErrForbidden
	}
	if e.cancel != nil && (e.record.State == "running" || e.record.State == "queued") {
		e.cancel()
		e.record.State = "cancelling"
		e.record.Phase = "cancel pending"
		select {
		case m.dirty <- struct{}{}:
		default:
		}
	}
	return e.record, nil
}

// Release authorizes a later explicit submission; it never starts a job itself.
func (m *Manager) Release(id, principal string, owner bool) (Record, error) {
	if !m.mutation.TryLock() {
		return Record{}, ErrBusy
	}
	defer m.mutation.Unlock()
	m.mu.Lock()
	e := m.entries[id]
	if e == nil {
		m.mu.Unlock()
		return Record{}, os.ErrNotExist
	}
	if !owner && e.record.Identity.Owner != principal {
		m.mu.Unlock()
		return Record{}, ErrForbidden
	}
	old := e.record
	if old.State != "quarantined" {
		m.mu.Unlock()
		return old, errors.New("job is not quarantined")
	}
	r := old
	r.State = "released"
	r.Card = false
	r.Failures = 0
	r.Reason = "explicit release; waiting for resubmission"
	m.mu.Unlock()
	rows := m.records()
	for n := range rows {
		if rows[n].ID == id {
			rows[n] = r
		}
	}
	if err := m.persistRows(rows); err != nil {
		return old, err
	}
	m.mu.Lock()
	e.record = r
	m.mu.Unlock()
	return r, nil
}

func (m *Manager) persist() error {
	return m.persistRows(m.records())
}

func (m *Manager) persistRows(rows []Record) error {
	b, err := json.Marshal(rows)
	if err != nil {
		return err
	}
	err = os.MkdirAll(filepath.Dir(m.o.Path), 0700)
	if err == nil {
		var f *os.File
		f, err = os.OpenFile(m.o.Path+".tmp", os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
		if err == nil {
			_, err = f.Write(b)
			if err == nil {
				err = f.Sync()
			}
			ce := f.Close()
			if err == nil {
				err = ce
			}
		}
		if err == nil {
			err = os.Rename(m.o.Path+".tmp", m.o.Path)
		}
	}
	m.persistence.Store(err == nil)
	return err
}

func (m *Manager) run() {
	defer m.workerDone()
	defer m.containPanic()
	for {
		select {
		case w := <-m.foreground:
			m.execute(w)
			continue
		default:
		}
		select {
		case <-m.ctx.Done():
			return
		case w := <-m.queue:
			m.execute(w)
		case w := <-m.foreground:
			m.execute(w)
		}
	}
}

func (m *Manager) containPanic() {
	if recover() != nil {
		m.workerFailed.Store(true)
		m.persistence.Store(false)
		m.stop()
	}
}
func (m *Manager) execute(w work) {
	m.mutation.Lock()
	m.mu.Lock()
	e := m.entries[w.id]
	e.record.State = "running"
	e.record.Phase = "opening"
	m.mu.Unlock()
	err := m.persist()
	m.mutation.Unlock()
	if err != nil {
		err = ErrPersistence
	}
	var digest string
	if err == nil {
		digest, err = m.audit(w)
	}
	m.mutation.Lock()
	defer m.mutation.Unlock()
	m.mu.Lock()
	r := &e.record
	r.Heartbeat = time.Now()
	r.Digest = digest
	switch {
	case w.ctx.Err() != nil:
		r.State = "cancelled"
		r.Phase = "cancelled"
	case errors.Is(err, ErrForbidden):
		r.State = "revoked"
		r.Phase = "authorization revoked"
		r.Reason = "authorization changed before placement"
	case errors.Is(err, ErrDegraded), errors.Is(err, ErrDeferred), errors.Is(err, ErrPersistence):
		r.State = "paused"
		r.Phase = "deferred"
		r.Reason = "audit deferred; resubmission required"
	case err != nil:
		r.State = "failed"
		r.Phase = "failed"
		r.Failures++
		r.Reason = "audit failed"
		r.Fingerprint = fingerprint(*r, fmt.Sprintf("%T", err))
		if r.Failures >= m.o.Failures {
			r.State = "quarantined"
			r.Card = r.Foreground
		}
	default:
		r.State = "completed"
		r.Phase = "verified"
	}
	e.cancel = nil
	m.mu.Unlock()
	m.persist()
}
func (m *Manager) audit(w work) (digest string, err error) {
	defer func() {
		if recover() != nil {
			err = errors.New("audit worker panic")
		}
	}()
	if w.task.Cleanup != nil {
		defer w.task.Cleanup()
	}
	if err = w.ctx.Err(); err != nil {
		return "", err
	}
	m.mu.Lock()
	foreground := m.entries[w.id].record.Foreground
	m.mu.Unlock()
	if !foreground && m.Degraded() {
		return "", ErrDegraded
	}
	r, err := w.task.Open(w.ctx)
	if err != nil {
		return "", err
	}
	closed := false
	defer func() {
		if !closed {
			r.Close()
		}
	}()
	hash := sha256.New()
	buf := make([]byte, ChunkSize)
	var processed int64
	last := time.Now()
	next := last
	for {
		if err = w.ctx.Err(); err != nil {
			return "", err
		}
		if !foreground && m.Degraded() {
			return "", ErrDegraded
		}
		if delay := time.Until(next); delay > 0 {
			t := time.NewTimer(delay)
			select {
			case <-w.ctx.Done():
				t.Stop()
				return "", w.ctx.Err()
			case <-t.C:
			}
		}
		n, re := r.Read(buf)
		if n > 0 {
			if m.totalExceeded(w.id, processed+int64(n)) {
				return "", errors.New("audit exceeds declared size")
			}
			hash.Write(buf[:n])
			processed += int64(n)
			next = time.Now().Add(time.Duration(int64(n) * int64(time.Second) / m.o.BytesPerSecond))
		}
		if time.Since(last) >= time.Second || re != nil {
			m.mu.Lock()
			e := m.entries[w.id]
			e.record.Bytes = processed
			e.record.Heartbeat = time.Now()
			if e.record.State != "cancelling" {
				e.record.Phase = "hashing"
			}
			m.mu.Unlock()
			last = time.Now()
		}
		if re == io.EOF {
			m.mu.Lock()
			total := m.entries[w.id].record.Total
			m.mu.Unlock()
			if total > 0 && processed != total {
				return "", io.ErrUnexpectedEOF
			}
			digest = hex.EncodeToString(hash.Sum(nil))
			if w.task.ExpectedDigest != "" && digest != w.task.ExpectedDigest {
				return "", errors.New("audit digest mismatch")
			}
			if err = r.Close(); err != nil {
				return "", err
			}
			closed = true
			if err = w.ctx.Err(); err != nil {
				return "", err
			}
			if w.task.Commit != nil {
				if err = w.task.Commit(w.ctx); err != nil {
					return "", err
				}
			}
			return digest, nil
		}
		if re != nil {
			return "", re
		}
		if n == 0 {
			return "", io.ErrNoProgress
		}
	}
}

func (m *Manager) totalExceeded(id string, bytes int64) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	total := m.entries[id].record.Total
	return total > 0 && bytes > total
}

// Close cancels owned workers. It does not wait on uninterruptible OS calls.
func (m *Manager) Close() { m.stop() }

// Done closes after both the audit and persistence workers have exited.
func (m *Manager) Done() <-chan struct{} { return m.done }

func (m *Manager) workerDone() {
	if m.workers.Add(-1) == 0 {
		close(m.done)
	}
}
