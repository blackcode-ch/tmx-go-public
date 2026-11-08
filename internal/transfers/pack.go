package transfers

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

func (s *Service) pack(ctx context.Context, r Record) (Export, error) {
	ticket, err := newTicket()
	if err != nil {
		return Export{}, err
	}
	if err = os.MkdirAll(s.Exports.dir, 0700); err != nil {
		return Export{}, err
	}
	out := filepath.Join(s.Exports.dir, ticket+".zip")
	ok := false
	defer func() {
		if !ok {
			_ = os.Remove(out)
		}
	}()
	// Estimate first, refusing before a pack can consume the disk reserve.
	var total int64
	walk := func(fn func(string, fs.DirEntry) error) error {
		return filepath.WalkDir(r.str("src"), func(p string, d fs.DirEntry, e error) error {
			if e != nil {
				return e
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if p == r.str("src") {
				return nil
			}
			if e = s.sourceAllowed(p); e != nil {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			return fn(p, d)
		})
	}
	if err = walk(func(p string, d fs.DirEntry) error {
		if !d.IsDir() {
			st, e := d.Info()
			if e != nil {
				return e
			}
			if st.Mode().IsRegular() {
				total += st.Size()
			}
		}
		return nil
	}); err != nil {
		return Export{}, err
	}
	if total > MaxFile {
		return Export{}, fail(413, "folder exceeds copy size cap")
	}
	if err = s.room(s.Exports.dir, total+(10<<30)); err != nil {
		return Export{}, err
	}
	f, err := os.OpenFile(out, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return Export{}, err
	}
	z := zip.NewWriter(f)
	var written int64
	mtimes := map[string]float64{}
	err = walk(func(p string, d fs.DirEntry) error {
		st, e := d.Info()
		if e != nil {
			return e
		}
		if !st.IsDir() && !st.Mode().IsRegular() {
			return nil
		}
		rel, e := filepath.Rel(r.str("src"), p)
		if e != nil {
			return e
		}
		name := filepath.ToSlash(rel)
		if name == ".tmx-mtimes.json" {
			return nil
		}
		h, e := zip.FileInfoHeader(st)
		if e != nil {
			return e
		}
		h.Name = name
		h.Method = zip.Store
		if st.IsDir() {
			h.Name += "/"
		} else if st.ModTime().Year() < 1980 {
			mtimes[name] = float64(st.ModTime().UnixNano()) / 1e9
			h.SetModTime(time.Date(1980, 1, 1, 0, 0, 0, 0, time.UTC))
		}
		w, e := z.CreateHeader(h)
		if e != nil || st.IsDir() {
			return e
		}
		in, e := os.Open(p)
		if e != nil {
			return e
		}
		defer in.Close()
		buf := make([]byte, 1<<20)
		for {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if e = s.room(s.Exports.dir, 5<<30); e != nil {
				return e
			}
			n, re := in.Read(buf)
			if n > 0 {
				written += int64(n)
				if written > MaxFile {
					return fail(413, "folder grew beyond copy size cap while packing")
				}
				if _, e = w.Write(buf[:n]); e != nil {
					return e
				}
			}
			if re == io.EOF {
				break
			}
			if re != nil {
				return re
			}
		}
		return nil
	})
	if len(mtimes) > 0 && err == nil {
		var w io.Writer
		w, err = z.CreateHeader(&zip.FileHeader{Name: ".tmx-mtimes.json", Method: zip.Store})
		if err == nil {
			err = json.NewEncoder(w).Encode(mtimes)
		}
	}
	ze := z.Close()
	fe := f.Close()
	if err == nil {
		err = ze
	}
	if err == nil {
		err = fe
	}
	if err != nil {
		return Export{}, err
	}
	st, err := os.Stat(out)
	if err != nil {
		return Export{}, err
	}
	if st.Size() > MaxFile {
		return Export{}, errors.New("packed folder exceeds size cap")
	}
	sum, err := hashFile(out)
	if err != nil {
		return Export{}, err
	}
	e := Export{Ticket: ticket, Path: normalizePath(out), Name: r.str("name"), Kind: "folder", Size: st.Size(), SHA256: sum, ModTime: st.ModTime(), To: r.str("to"), Dest: r.str("dest"), ID: r.str("id"), Created: time.Now(), Temp: true}
	err = s.Exports.save(e)
	ok = err == nil
	return e, err
}
