package antechamber

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/config"
)

// Item states.
const (
	StatePending    = "pending"
	StateInspecting = "inspecting"
	StateNeedsHuman = "needs_human" // shown for a pending item whose verdict is needs_human
	StateApproved   = "approved"
	StateReleased   = "released"
	StateRefused    = "refused"
	StateBlocked    = "blocked" // shown for an item whose verdict is blocked
	StateExpired    = "expired"
)

// Origin says where an item came from and where it is meant to go.
type Origin struct {
	Kind  string `json:"kind,omitempty"`  // transfer, attachment, person …
	Trust string `json:"trust,omitempty"` // own | person
	From  string `json:"from,omitempty"`  // agent@machine or a person
	Dest  string `json:"dest,omitempty"`  // the folder it is meant for, when the caller knows it
	Cloud bool   `json:"cloud,omitempty"` // sent by a cloud box: its copies always wait for a human
}

// Event is one line of an item's history.
type Event struct {
	T     int64  `json:"t"`
	Event string `json:"event"`
	By    string `json:"by"`
}

// Item is the record kept in <root>/<id>/item.json (Python's item dict, same JSON names).
type Item struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	OriginalName string   `json:"original_name"`
	Size         int64    `json:"size"`
	SHA256       string   `json:"sha256"`
	Origin       Origin   `json:"origin"`
	Trust        string   `json:"trust"`
	Folder       bool     `json:"folder"`
	State        string   `json:"state"`
	Verdict      string   `json:"verdict"` // "" until inspected (Python: null)
	Kind         string   `json:"kind"`
	Reasons      []Reason `json:"reasons"`
	Members      int      `json:"members"`
	Created      float64  `json:"created"`
	Updated      float64  `json:"updated"`
	Inspected    float64  `json:"inspected,omitempty"`
	Approved     float64  `json:"approved,omitempty"`
	ApprovedBy   string   `json:"approved_by,omitempty"`
	RefusedBy    string   `json:"refused_by,omitempty"`
	Released     float64  `json:"released,omitempty"`
	ReleasedTo   string   `json:"released_to,omitempty"`
	ReplacedTo   string   `json:"replaced_to,omitempty"`
	History      []Event  `json:"history"`
}

// Antechamber is the quarantine store of this machine. Safe for concurrent use.
type Antechamber struct {
	OwnerNames []string // local profile and optional config, copied at service startup and passed to isolated inspectors
	Root       string
	Limits     Limits
	Isolate    bool   // inspect in a child process (tmx antechamber inspect); false = in this process
	LogPath    string // audit log (JSON lines)
	Host       string
	Exe        string           // the tmx executable for the child; "" = os.Executable()
	Now        func() time.Time // the clock (tests)
	AV         AVScanner

	mu sync.Mutex
}

// New opens (creating it if needed) the store at root with Python's defaults: isolated
// inspection, DefaultLimits, the audit log in root/antechamber.log.
func New(root, host string) (*Antechamber, error) {
	if err := os.MkdirAll(filepath.Join(root, "uploads"), 0o700); err != nil {
		return nil, fmt.Errorf("antechamber: %w", err)
	}
	if host == "" {
		host = "this-machine"
	}
	return &Antechamber{Root: root, Limits: DefaultLimits(), Isolate: true,
		LogPath: filepath.Join(root, "antechamber.log"), Host: host, Now: time.Now, AV: SystemAV{}}, nil
}

func (a *Antechamber) now() float64 {
	return float64(a.Now().UnixNano()) / 1e9
}

// ---- bookkeeping

func (a *Antechamber) audit(event string, it *Item, kw map[string]any) {
	rec := map[string]any{"t": math.Round(a.now()), "event": event}
	if it != nil {
		rec["id"], rec["name"], rec["size"], rec["sha256"] = it.ID, it.Name, it.Size, it.SHA256
		rec["verdict"], rec["state"], rec["from"] = it.Verdict, it.State, it.Origin.From
	}
	for k, v := range kw {
		rec[k] = v
	}
	line, err := json.Marshal(rec)
	if err != nil {
		return
	}
	f, err := os.OpenFile(a.LogPath, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(append(line, '\n'))
}

var sha256Rx = regexp.MustCompile(`^[0-9a-f]{64}$`)

// readKnownBad reads a known-bad list: one SHA-256 per line, "# note" comments allowed.
func readKnownBad(path string) map[string]bool {
	out := map[string]bool{}
	f, err := os.Open(path)
	if err != nil {
		return out
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		h, _, _ := strings.Cut(sc.Text(), "#")
		if h = strings.ToLower(strings.TrimSpace(h)); sha256Rx.MatchString(h) {
			out[h] = true
		}
	}
	return out
}

func (a *Antechamber) knownBadFile() string { return filepath.Join(a.Root, "known_bad.txt") }

// KnownBad is the set of SHA-256 digests that are always blocked.
func (a *Antechamber) KnownBad() map[string]bool { return readKnownBad(a.knownBadFile()) }

func (a *Antechamber) addKnownBad(sum, note string) error {
	f, err := os.OpenFile(a.knownBadFile(), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("antechamber: known-bad list: %w", err)
	}
	defer f.Close()
	line := sum + "\n"
	if note != "" {
		line = sum + "  # " + note + "\n"
	}
	_, err = f.WriteString(line)
	return err
}

var idRx = regexp.MustCompile(`^[0-9a-f]{32}$`)

func (a *Antechamber) dir(id string) (string, error) {
	if !idRx.MatchString(id) {
		return "", errf(404, "no quarantine item %s", pyRepr(id))
	}
	return filepath.Join(a.Root, id), nil
}

func (a *Antechamber) save(it *Item) error {
	d, err := a.dir(it.ID)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(it, "", " ")
	if err != nil {
		return err
	}
	if err := config.WriteFileAtomic(filepath.Join(d, "item.json"), data, 0o600); err != nil {
		return fmt.Errorf("antechamber: save %s: %w", it.ID, err)
	}
	return nil
}

// Get reads one item.
func (a *Antechamber) Get(id string) (Item, error) {
	var it Item
	d, err := a.dir(id)
	if err != nil {
		return it, err
	}
	data, err := os.ReadFile(filepath.Join(d, "item.json"))
	if err == nil {
		err = json.Unmarshal(data, &it)
	}
	if err != nil {
		return Item{}, errf(404, "no quarantine item %s", pyRepr(id))
	}
	return it, nil
}

// List returns the items (all, or those in state), newest first.
func (a *Antechamber) List(state string) []Item {
	var out []Item
	ents, _ := os.ReadDir(a.Root)
	for _, e := range ents {
		if !e.IsDir() || !idRx.MatchString(e.Name()) {
			continue
		}
		it, err := a.Get(e.Name())
		if err != nil {
			continue
		}
		if state == "" || it.State == state {
			out = append(out, it)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Created > out[j].Created })
	return out
}

func treeSize(root string) int64 {
	var n int64
	_ = filepath.WalkDir(root, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			if info, err := d.Info(); err == nil {
				n += info.Size()
			}
		}
		return nil
	})
	return n
}

// Usage is every byte the store holds on disk (physical retention).
func (a *Antechamber) Usage() int64 { return treeSize(a.Root) }

// QuotaUsage counts active quarantine bytes; retained decisions do not consume admission quota.
// Uploads reserve their announced size; unreadable item metadata is counted conservatively.
func (a *Antechamber) QuotaUsage() int64 {
	var total int64
	ents, _ := os.ReadDir(a.Root)
	for _, e := range ents {
		if !e.IsDir() || !idRx.MatchString(e.Name()) {
			continue
		}
		if it, err := a.Get(e.Name()); err == nil {
			switch it.State {
			case StateApproved, StateRefused, StateReleased, StateExpired:
				continue
			}
		}
		total += treeSize(filepath.Join(a.Root, e.Name()))
	}
	parts, _ := filepath.Glob(filepath.Join(a.Root, "uploads", "*.part"))
	for _, p := range parts {
		st, err := os.Stat(p)
		if err != nil {
			continue // concurrently admitted
		}
		size := st.Size()
		var meta struct {
			Size int64 `json:"size"`
		}
		if data, err := os.ReadFile(strings.TrimSuffix(p, ".part") + ".json"); err == nil && json.Unmarshal(data, &meta) == nil {
			size = max(size, meta.Size)
		}
		total += size
	}
	return total
}

func (a *Antechamber) keep(path string) int64 {
	if a.Limits.MinFree != nil {
		return *a.Limits.MinFree
	}
	return ReserveAt(path)
}

// room refuses an item over the size limit, over the quarantine quota or the free-space reserve.
// An unreadable free-space figure does not refuse.
func (a *Antechamber) room(size int64, trust string) error {
	limit := a.Limits.MaxFilePerson
	if trust == Own {
		limit = a.Limits.MaxFileOwn
	}
	if size > limit {
		return errf(413, "%d MB is over the %d MB limit for one item", size>>20, limit>>20)
	}
	if a.QuotaUsage()+size > a.Limits.QuarantineMax {
		return errf(507, "the quarantine on this machine is full; ask its owner to accept or refuse waiting items")
	}
	if free, _, err := diskSpace(a.Root); err == nil && int64(free)-size < a.keep(a.Root) {
		return errf(507, "not enough free disk space on this machine to receive it safely")
	}
	return nil
}

// ---- receiving

// AdmitBytes admits data in memory as name.
func (a *Antechamber) AdmitBytes(data []byte, name string, origin Origin, folder bool) (Item, error) {
	sum := sha256.Sum256(data)
	return a.admit(name, int64(len(data)), hex.EncodeToString(sum[:]), origin, folder, func(part string) error {
		return os.WriteFile(part, data, 0o600)
	})
}

// AdmitPath admits a file already on this disk (e.g. received by another transport): copied in,
// or moved with move (no second copy of a big transfer), then inspected. Pass the sha256 the
// transport already checked to skip hashing twice; it is verified anyway.
func (a *Antechamber) AdmitPath(path, name string, origin Origin, folder, moveIn bool, sum string) (Item, error) {
	st, err := os.Stat(path)
	if err != nil {
		return Item{}, fmt.Errorf("antechamber: admit: %w", err)
	}
	if sum == "" {
		if sum, err = fileSHA256(path); err != nil {
			return Item{}, fmt.Errorf("antechamber: admit: %w", err)
		}
	}
	if name == "" {
		name = filepath.Base(path)
	}
	return a.admit(name, st.Size(), sum, origin, folder, func(part string) error {
		os.Remove(part)
		if moveIn {
			if err := rename(path, part); err == nil {
				return nil
			}
			if err := copyFile(path, part, 0o600); err != nil { // another volume: copy, then remove the source
				return err
			}
			return os.Remove(path)
		}
		return copyFile(path, part, 0o600)
	})
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
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

func (a *Antechamber) admit(name string, size int64, sum string, origin Origin, folder bool, fill func(part string) error) (Item, error) {
	if origin.Trust != Person {
		origin.Trust = Own
	}
	sum = strings.ToLower(sum)
	if size < 0 || !sha256Rx.MatchString(sum) {
		return Item{}, errf(400, "an upload needs its size and SHA-256")
	}
	if len([]rune(name)) > 1000 {
		name = string([]rune(name)[:1000])
	}
	up := randHex(16)
	uploads := filepath.Join(a.Root, "uploads")
	part, meta := filepath.Join(uploads, up+".part"), filepath.Join(uploads, up+".json")
	drop := func() { os.Remove(part); os.Remove(meta) }

	a.mu.Lock()
	if err := a.room(size, origin.Trust); err != nil {
		a.mu.Unlock()
		return Item{}, err
	}
	// The announced size is reserved in the quota while the bytes arrive (as Python's uploads).
	m, _ := json.Marshal(map[string]any{"id": up, "size": size, "touched": a.now()})
	err := os.MkdirAll(uploads, 0o700)
	if err == nil {
		err = os.WriteFile(part, nil, 0o600)
	}
	if err == nil {
		err = os.WriteFile(meta, m, 0o600)
	}
	a.mu.Unlock()
	if err != nil {
		drop()
		return Item{}, fmt.Errorf("antechamber: admit: %w", err)
	}

	if err := fill(part); err != nil {
		drop()
		return Item{}, fmt.Errorf("antechamber: admit: %w", err)
	}
	st, err := os.Stat(part)
	if err != nil || st.Size() != size {
		drop()
		got := int64(-1)
		if st != nil {
			got = st.Size()
		}
		return Item{}, errf(409, "incomplete: %d of %d bytes arrived", got, size)
	}
	digest, err := fileSHA256(part)
	if err != nil || digest != sum {
		drop()
		return Item{}, errf(409, "damaged in transit: the SHA-256 does not match; nothing was kept, send it again")
	}
	a.mu.Lock()
	id := randHex(16)
	d := filepath.Join(a.Root, id)
	err = os.Mkdir(d, 0o700)
	if err == nil {
		err = os.Rename(part, filepath.Join(d, "blob"))
	}
	drop()
	a.mu.Unlock()
	if err != nil {
		os.RemoveAll(d)
		return Item{}, fmt.Errorf("antechamber: admit: %w", err)
	}
	return a.admitDir(id, name, size, digest, origin, folder)
}

var zipSuffixRx = regexp.MustCompile(`(?i)[.]zip$`)

func (a *Antechamber) admitDir(id, name string, size int64, digest string, origin Origin, folder bool) (Item, error) {
	d := filepath.Join(a.Root, id)
	blob := filepath.Join(d, "blob")
	_ = os.Chmod(blob, 0o400)
	safe, _ := CleanName(name)
	now := a.now()
	it := &Item{ID: id, Name: safe, OriginalName: name, Size: size, SHA256: digest, Origin: origin,
		Trust: origin.Trust, Folder: folder, State: StateInspecting, Reasons: []Reason{},
		Created: now, Updated: now, History: []Event{{T: int64(math.Round(now)), Event: "received", By: or(origin.From, "?")}}}
	if err := a.save(it); err != nil {
		return Item{}, err
	}
	ctxPath := ""
	if dest := origin.Dest; dest != "" {
		ctxPath = dest
		if folder {
			ctxPath = strings.TrimRight(dest, "\\/") + "/" + zipSuffixRx.ReplaceAllString(safe, "")
		}
	}
	rep := a.inspect(d, name, it.Trust, folder, size, ctxPath)
	now = a.now()
	it.Verdict, it.Kind, it.Reasons, it.Members = rep.Verdict, rep.Kind, rep.Reasons, rep.Members
	it.State, it.Inspected, it.Updated = StatePending, now, now
	it.History = append(it.History, Event{T: int64(math.Round(now)), Event: "inspected: " + rep.Verdict, By: a.Host})
	if err := a.save(it); err != nil {
		return Item{}, err
	}
	a.audit("admitted", it, nil)
	return *it, nil
}

func or(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// ---- the human's decision

// Approve lets a pending item be released. A blocked item can never be approved.
func (a *Antechamber) Approve(id, by string) (Item, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	it, err := a.Get(id)
	if err != nil {
		return it, err
	}
	if it.Verdict == Blocked {
		return it, errf(409, "a blocked item can never be approved")
	}
	if it.State != StatePending {
		return it, errf(409, "item is %s, not waiting for a decision", it.State)
	}
	now := a.now()
	it.State, it.Approved, it.ApprovedBy, it.Updated = StateApproved, now, by, now
	it.History = append(it.History, Event{T: int64(math.Round(now)), Event: "approved", By: by})
	if err := a.save(&it); err != nil {
		return it, err
	}
	a.audit("approved", &it, map[string]any{"by": by})
	return it, nil
}

// Refuse refuses an item; blockHash also puts its SHA-256 on the known-bad list.
func (a *Antechamber) Refuse(id, by string, blockHash bool) (Item, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	it, err := a.Get(id)
	if err != nil {
		return it, err
	}
	if it.State == StateReleased || it.State == StateRefused {
		return it, errf(409, "item is already %s", it.State)
	}
	now := a.now()
	it.State, it.RefusedBy, it.Updated = StateRefused, by, now
	ev := "refused"
	if blockHash {
		ev += " and blocked"
	}
	it.History = append(it.History, Event{T: int64(math.Round(now)), Event: ev, By: by})
	if err := a.save(&it); err != nil {
		return it, err
	}
	if blockHash {
		if err := a.addKnownBad(it.SHA256, fmt.Sprintf("refused by %s: %s", by, it.Name)); err != nil {
			return it, err
		}
	}
	a.audit("refused", &it, map[string]any{"by": by, "block_hash": blockHash})
	return it, nil
}

// Releasable: never blocked; approved, or pending and clean.
func Releasable(it Item) bool {
	return it.Verdict != Blocked && (it.State == StateApproved || (it.State == StatePending && it.Verdict == Clean))
}

// ReleaseOptions are Release's optional settings.
type ReleaseOptions struct {
	By          string
	Name        string // the name to place it as (made safe); "" = the name it arrived with
	ReplaceInto string // an existing folder the old file of the same name is MOVED to (never deleted)
}

// Release places a clean or approved item into an existing folder. It never overwrites: an
// existing target becomes 'name (2).ext', or, with ReplaceInto, is first moved there and the item
// takes its exact name. A folder item is extracted safely. It returns the placed path.
func (a *Antechamber) Release(id, destDir string, o ReleaseOptions) (string, error) {
	if o.ReplaceInto != "" {
		if st, err := os.Stat(o.ReplaceInto); err != nil || !st.IsDir() {
			return "", errf(400, "backup folder %s is not an existing folder", o.ReplaceInto)
		}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	it, err := a.Get(id)
	if err != nil {
		return "", err
	}
	if !Releasable(it) {
		if it.Verdict == Blocked {
			return "", errf(403, "cannot place %s: blocked", it.Name)
		}
		why := it.State
		if it.State == StatePending {
			why = fmt.Sprintf("%s (%s): it needs the human's approval first", it.State, it.Verdict)
		}
		return "", errf(409, "cannot place %s: %s", it.Name, why)
	}
	if st, err := os.Stat(destDir); err != nil || !st.IsDir() {
		return "", errf(400, "destination %s is not an existing folder", destDir)
	}
	d, _ := a.dir(id)
	blob := filepath.Join(d, "blob")
	if !lexists(blob) {
		return "", errf(410, "the file is no longer in the quarantine")
	}
	if free, _, err := diskSpace(destDir); err == nil && int64(free)-it.Size < a.keep(destDir)/2 {
		return "", errf(507, "not enough free disk space at the destination")
	}
	if it.State == StateApproved && a.now()-it.Inspected > a.Limits.RescanAfterS && a.AV != nil {
		f := a.AV.Scan(blob)
		if f.Level(it.Trust) == Blocked || !lexists(blob) {
			it.Verdict, it.Updated = Blocked, a.now()
			it.Reasons = append([]Reason{{Level: Blocked, Code: f.Code, Text: f.Text}}, it.Reasons...)
			_ = a.save(&it)
			return "", errf(403, "cannot place %s: %s", it.Name, f.Text)
		}
	}
	referrer := "tmx://" + or(it.Origin.From, "another-machine")
	target := it.Name
	switch {
	case o.Name != "":
		target, _ = CleanName(o.Name)
	case it.Folder:
		// Cleaned again after the suffix goes: "..zip" would otherwise name the destination
		// itself, "...zip" its parent (o5, #256).
		target, _ = CleanName(or(zipSuffixRx.ReplaceAllString(it.Name, ""), "folder"))
	default:
		target, _ = CleanName(it.Name)
	}
	want := filepath.Join(destDir, target)
	// The sender picks the name: whatever it is, the copy lands directly inside destDir.
	if target == "" || target == "." || target == ".." || filepath.Base(target) != target ||
		filepath.Dir(want) != filepath.Clean(destDir) {
		return "", errf(400, "the name %s does not name a place inside %s", pyRepr(target), destDir)
	}
	backup := ""
	if o.ReplaceInto != "" && lexists(want) {
		if backup, err = freeName(o.ReplaceInto, target); err != nil {
			return "", err
		}
		if err := move(want, backup); err != nil { // kept, never deleted; another drive is fine
			return "", err
		}
	}
	var placed string
	if it.Folder {
		placed, err = SafeExtract(blob, want, a.Limits, referrer, it.Trust)
	} else {
		var to string
		if to, err = freeName(destDir, target); err == nil {
			placed, err = placeFile(blob, to, referrer)
		}
	}
	if err != nil {
		if backup != "" && !lexists(want) {
			_ = move(backup, want) // put the old one back
		}
		return "", err
	}
	now := a.now()
	it.ReplacedTo = backup
	it.State, it.ReleasedTo, it.Released, it.Updated = StateReleased, placed, now, now
	it.History = append(it.History, Event{T: int64(math.Round(now)), Event: "released to " + placed, By: or(o.By, "?")})
	if err := a.save(&it); err != nil {
		return placed, err
	}
	a.audit("released", &it, map[string]any{"to": placed, "by": o.By})
	return placed, nil
}

// ---- housekeeping (call every few minutes; Python's daemon runs it hourly)

// Purge deletes what is no longer needed and returns how many items and uploads it removed.
// Waiting items expire after keep_days (kept another keep_days); refused, expired and blocked
// items are deleted keep_days after their last change; released items after released_keep_h.
func (a *Antechamber) Purge() int {
	now, n := a.now(), 0
	keep := a.Limits.KeepDays * 86400
	a.mu.Lock()
	defer a.mu.Unlock()
	metas, _ := filepath.Glob(filepath.Join(a.Root, "uploads", "*.json"))
	for _, p := range metas {
		var meta struct {
			Touched float64 `json:"touched"`
		}
		if data, err := os.ReadFile(p); err == nil {
			_ = json.Unmarshal(data, &meta)
		}
		if now-meta.Touched > a.Limits.UploadIdleS {
			os.Remove(strings.TrimSuffix(p, ".json") + ".part")
			os.Remove(p)
			n++
		}
	}
	for _, it := range a.List("") {
		updated := it.Updated
		if updated == 0 {
			updated = it.Created
		}
		age := now - updated
		switch {
		case (it.State == StatePending || it.State == StateApproved || it.State == StateInspecting) && age > keep:
			it.State, it.Updated = StateExpired, now
			it.History = append(it.History, Event{T: int64(math.Round(now)), Event: "expired", By: a.Host})
			if a.save(&it) == nil {
				a.audit("expired", &it, nil)
			}
		case (it.State == StateRefused || it.State == StateExpired || it.Verdict == Blocked) && age > keep,
			it.State == StateReleased && age > a.Limits.ReleasedKeepH*3600:
			a.remove(it.ID)
			n++
		}
	}
	return n
}

// Discard lets the owner free space now: a refused or expired item's copy is deleted before its
// keep_days. Never an item waiting for a decision, never a released one's record.
func (a *Antechamber) Discard(id, by string) (Item, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	it, err := a.Get(id)
	if err != nil {
		return it, err
	}
	if it.State != StateRefused && it.State != StateExpired {
		return it, errf(409, "only a refused or expired item can be discarded (this one is %s)", it.State)
	}
	a.remove(id)
	a.audit("discarded", &it, map[string]any{"by": by})
	return it, nil
}

func (a *Antechamber) remove(id string) {
	d, err := a.dir(id)
	if err != nil {
		return
	}
	_ = os.Chmod(filepath.Join(d, "blob"), 0o600)
	if err := os.RemoveAll(d); err != nil && !errors.Is(err, fs.ErrNotExist) {
		a.audit("remove-failed", nil, map[string]any{"id": id, "error": err.Error()})
	}
}
