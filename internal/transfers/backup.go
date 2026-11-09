package transfers

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/antechamber"
	"github.com/blackcode-ch/tmx-go/internal/config"
)

func (s *Service) backupDestination(r, b Record) error {
	root := filepath.Join(s.Home, "backups")
	if b.flag("trusted_backup") {
		root = expand(b.str("dest"))
		registered := false
		for _, p := range s.TrustedBackupRoots {
			if samePath(root, expand(p)) {
				registered = true
			}
		}
		if !filepath.IsAbs(root) || !registered {
			return fail(403, "destination is not a registered backup root")
		}
		if err := s.destinationAllowedRoot(root); err != nil {
			return err
		}
	}
	if err := noLinks(root); err != nil {
		return err
	}
	source := regexp.MustCompile(`[^a-z0-9_-]`).ReplaceAllString(r.str("from"), "_")
	if len(source) > 40 {
		source = source[:40]
	}
	var used int64
	_ = filepath.WalkDir(filepath.Join(root, source), func(p string, d os.DirEntry, e error) error {
		if e == nil && !d.IsDir() {
			if st, e := d.Info(); e == nil {
				used += st.Size()
			}
		}
		return nil
	})
	if used+r.size() > 200<<30 {
		return fail(507, "backups from this source exceed 200 GB")
	}
	incoming := filepath.Join(root, ".incoming")
	if err := config.MkdirSecret(incoming); err != nil {
		return err
	}
	r["dest"] = filepath.Join(root, source, time.Now().Format("20060102-150405")+"-"+newID()[:6])
	r["backup"], r["trusted_backup"], r["backup_root"], r["part_dir"], r["force"] = true, b.flag("trusted_backup"), root, incoming, false
	return nil
}
func (s *Service) destinationAllowedRoot(root string) error {
	home, _ := os.UserHomeDir()
	if filepath.Dir(root) == root || samePath(root, home) || samePath(filepath.Dir(root), home) {
		return fail(403, "backup root is too broad")
	}
	if why := ForbiddenDestination(root, s.Home, s.Install, s.Protected...); why != "" {
		return fail(403, why)
	}
	if why := ForbiddenSource(root, s.Home, s.Install, append(s.Protected, s.SecretFolders...)...); why != "" {
		return fail(403, why)
	}
	for _, p := range append(CredentialStores(), s.SecretFolders...) {
		if within(p, root) {
			return fail(403, "backup root contains a credential or secret store")
		}
	}
	for _, p := range filepath.SplitList(os.Getenv("PATH")) {
		if p != "" && within(root, p) {
			return fail(403, "backup root must not be in an executable PATH folder")
		}
	}
	return noLinks(root)
}
func (s *Service) storeBackup(ctx context.Context, r Record, part string) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	dest := r.str("dest")
	if err := noLinks(dest); err != nil {
		return err
	}
	if r.flag("trusted_backup") {
		if err := s.destinationAllowedRoot(r.str("backup_root")); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0700); err != nil {
		return err
	}
	if err := os.Mkdir(dest, 0700); err != nil {
		return err
	}
	if err := config.MkdirSecret(dest); err != nil {
		return err
	}
	name := r.str("name")
	if r.str("kind") == "folder" {
		name += ".zip"
	}
	name += ".held"
	var inspection any
	if r.flag("trusted_backup") {
		report, err := s.auditBackup(ctx, part, r, dest)
		if err != nil {
			return err
		}
		inspection = report
		marker, _ := json.Marshal(Record{"transfer": r.str("id"), "sha256": r.str("sha256")})
		if err = config.WriteFileAtomic(filepath.Join(dest, "transfer.json"), marker, 0600); err != nil {
			return err
		}
	}
	target := filepath.Join(dest, name)
	if err := os.Rename(part, target); err != nil {
		return err
	}
	if runtime.GOOS == "windows" {
		if err := os.WriteFile(target+":Zone.Identifier", []byte("[ZoneTransfer]\r\nZoneId=3\r\nReferrerUrl=tmx-backup://"+r.str("from")+"\r\n"), 0600); err != nil {
			return err
		}
	}
	if err := os.Chmod(target, 0400); err != nil {
		return err
	}
	manifest := Record{"from": r.str("from"), "name": r.str("name"), "kind": r.str("kind"), "stored_as": name, "size": r.size(), "sha256": r.str("sha256"), "received": now(), "opened": false, "inspection": inspection, "note": "Stored unopened. Take it out with the antechamber, never load it here."}
	data, _ := json.Marshal(manifest)
	if err := config.WriteFileAtomic(filepath.Join(dest, "manifest.json"), data, 0600); err != nil {
		return err
	}
	r["state"], r["placed"], r["error"], r["inspection"] = "placed", target, nil, inspection
	if inspection != nil {
		r["report_url"] = "/api/transfers/" + r.str("id") + "/report"
	}
	return s.save(r)
}
func (s *Service) auditBackup(ctx context.Context, part string, r Record, dest string) (Record, error) {
	rows, err := os.OpenFile(filepath.Join(dest, "files.jsonl"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	page, err := os.OpenFile(filepath.Join(dest, "report.html"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return nil, err
	}
	defer page.Close()
	_, _ = io.WriteString(page, "<!doctype html><meta charset=utf-8><title>Trusted backup inspection</title><h1>Cold archive inspection</h1><p>No files extracted or executed.</p><table>")
	counts := Record{"files": 0, "flagged": 0, "bytes": int64(0)}
	record := func(reader io.Reader, name string, size int64) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		h := sha256.New()
		buf := make([]byte, 0, min(size, 64<<20))
		writer := &auditWriter{hash: h, buf: &buf, cap: 64 << 20}
		n, e := io.Copy(writer, io.LimitReader(reader, size+1))
		if e != nil {
			return e
		}
		if n != size {
			return fail(409, "backup member size differs")
		}
		inspector := antechamber.NewInspector(antechamber.Own, s.Antechamber.Limits, s.Antechamber.KnownBad(), "")
		rep := inspector.InspectBytes(buf, name, false).JSON()
		if size > 64<<20 {
			rep.Reasons = append(rep.Reasons, antechamber.Reason{Code: "scan-limit", Level: antechamber.NeedsHuman, Text: "inspection capped at 64 MB; full file hash verified"})
			if rep.Verdict == antechamber.Clean {
				rep.Verdict = antechamber.NeedsHuman
			}
		}
		if e = json.NewEncoder(rows).Encode(Record{"name": name, "size": n, "sha256": hex.EncodeToString(h.Sum(nil)), "inspection": rep}); e != nil {
			return e
		}
		counts["files"] = counts["files"].(int) + 1
		counts["bytes"] = counts["bytes"].(int64) + n
		if rep.Verdict != antechamber.Clean {
			counts["flagged"] = counts["flagged"].(int) + 1
			_, e = fmt.Fprintf(page, "<tr><td>%s</td><td>%s</td></tr>", html.EscapeString(name), html.EscapeString(fmt.Sprint(rep.Reasons)))
		}
		return e
	}
	if r.str("kind") == "folder" {
		z, e := zip.OpenReader(part)
		if e != nil {
			return nil, e
		}
		defer z.Close()
		var total uint64
		if len(z.File) > s.Antechamber.Limits.MaxMembers {
			return nil, fail(413, "backup archive has too many members")
		}
		for _, f := range z.File {
			if !f.FileInfo().IsDir() {
				if f.UncompressedSize64 > uint64(MaxFile) || f.UncompressedSize64 != f.CompressedSize64 {
					return nil, fail(413, "invalid or oversized stored backup member")
				}
				total += f.UncompressedSize64
				if total > uint64(MaxFile) {
					return nil, fail(413, "backup member totals exceed size cap")
				}
			}
		}
		for _, f := range z.File {
			if f.FileInfo().IsDir() {
				continue
			}
			if f.Method != zip.Store || f.Flags&1 != 0 {
				return nil, fail(400, "trusted folder backup requires unencrypted stored ZIP entries")
			}
			reader, e := f.Open()
			if e != nil {
				return nil, e
			}
			e = record(reader, f.Name, int64(f.UncompressedSize64))
			reader.Close()
			if e != nil {
				return nil, e
			}
		}
	} else {
		f, e := os.Open(part)
		if e != nil {
			return nil, e
		}
		defer f.Close()
		if e = record(f, r.str("name"), r.size()); e != nil {
			return nil, e
		}
	}
	av := s.Antechamber.AV.Scan(part)
	if err = json.NewEncoder(rows).Encode(Record{"archive_antivirus": Record{"code": av.Code, "text": av.Text, "level": av.Level(antechamber.Own)}}); err != nil {
		return nil, err
	}
	if !exists(part) {
		return nil, fail(409, "antivirus removed the incoming backup")
	}
	_, err = io.WriteString(page, "</table>")
	return counts, err
}

type auditWriter struct {
	hash io.Writer
	buf  *[]byte
	cap  int64
}

func (w *auditWriter) Write(b []byte) (int, error) {
	if left := w.cap - int64(len(*w.buf)); left > 0 {
		*w.buf = append(*w.buf, b[:min(int64(len(b)), left)]...)
	}
	return w.hash.Write(b)
}
