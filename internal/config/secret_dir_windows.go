package config

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

// mkdirSecret never sets security information on an existing object. The only
// directory DACL we supply is part of an exclusive CreateDirectory operation.
// Inspect every ancestor through a no-follow handle as well as by pathname.
// FILE_READ_ATTRIBUTES-only handles do not prevent concurrent ancestor renames:
// path-based creation still has a residual check/use race. Exclusive creation
// ensures this code never rewrites an existing object's DACL. Closing that race
// requires creation relative to a parent handle.
func mkdirSecret(dir string) (err error) {
	if err := testModePath(dir); err != nil {
		return err
	}
	defer func() {
		if err != nil {
			err = fmt.Errorf("%w; set TMXG_HOME to a plain local folder without junctions or symlinks", err)
			slog.Warn("config: refused secrets directory", "dir", dir, "err", err)
		}
	}()
	abs, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	volume := filepath.VolumeName(abs)
	if len(volume) != 2 || volume[1] != ':' || strings.ContainsAny(abs[len(volume):], ":") {
		return fmt.Errorf("unsafe secrets path: local drive path required")
	}
	parts := strings.Split(strings.TrimPrefix(abs[len(volume):], `\`), `\`)
	for _, part := range parts {
		if part == "" || strings.TrimRight(part, " .") != part {
			return fmt.Errorf("unsafe secrets path component %q", part)
		}
	}
	policy, err := secretDirectoryPolicy()
	if err != nil {
		return err
	}
	if err := policy.check(abs); err != nil {
		return err
	}
	var handles []windows.Handle
	defer func() {
		for i := len(handles) - 1; i >= 0; i-- {
			windows.CloseHandle(handles[i])
		}
	}()
	current := volume + `\`
	for i := -1; i < len(parts); i++ {
		if i >= 0 {
			current = filepath.Join(current, parts[i])
		}
		fi, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) && i >= 0 {
			if err := testModePath(current); err != nil {
				return err
			}
			name, e := windows.UTF16PtrFromString(current)
			if e != nil {
				return e
			}
			var sa *windows.SecurityAttributes
			if i == len(parts)-1 {
				sd, _, e := secretSD(true)
				if e != nil {
					return e
				}
				sa = securityAttributes(sd)
			}
			// A competing creator does not authorize changing its DACL. Inspect
			// the object it supplied using the same no-follow checks below.
			if e := windows.CreateDirectory(name, sa); e != nil && !errors.Is(e, windows.ERROR_ALREADY_EXISTS) {
				return &os.PathError{Op: "mkdir", Path: current, Err: e}
			}
			fi, err = os.Lstat(current)
		}
		if err != nil {
			return err
		}
		if !fi.IsDir() || fi.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("unsafe secrets ancestor: %s", current)
		}
		name, err := windows.UTF16PtrFromString(current)
		if err != nil {
			return err
		}
		attrs, err := windows.GetFileAttributes(name)
		if err != nil {
			return err
		}
		if attrs&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
			return fmt.Errorf("reparse point in secrets ancestry: %s", current)
		}
		h, err := windows.CreateFile(name, windows.FILE_READ_ATTRIBUTES,
			windows.FILE_SHARE_READ, nil, windows.OPEN_EXISTING,
			windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
		if err != nil {
			return &os.PathError{Op: "lock secrets ancestor", Path: current, Err: err}
		}
		handles = append(handles, h)
		var info windows.ByHandleFileInformation
		if err := windows.GetFileInformationByHandle(h, &info); err != nil {
			return err
		}
		if info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 || info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 {
			return fmt.Errorf("unsafe secrets ancestor changed during check: %s", current)
		}
		// Resolve short (8.3) names by handle, never by following a reparse
		// point. Check the eventual leaf before creating the next component.
		buf := make([]uint16, 32768)
		n, err := windows.GetFinalPathNameByHandle(h, &buf[0], uint32(len(buf)), 0)
		if err != nil {
			return err
		}
		if n >= uint32(len(buf)) {
			return errors.New("secrets path too long")
		}
		canonical := strings.TrimPrefix(windows.UTF16ToString(buf[:n]), `\\?\`)
		target := filepath.Join(append([]string{canonical}, parts[i+1:]...)...)
		if err := policy.check(target); err != nil {
			return err
		}
	}
	return nil
}

type secretDirPolicy struct {
	profile  string
	profiles string
	system   []string
}

func secretDirectoryPolicy() (secretDirPolicy, error) {
	var p secretDirPolicy
	var err error
	p.profile, err = windows.KnownFolderPath(windows.FOLDERID_Profile, windows.KF_FLAG_DONT_VERIFY)
	if err != nil {
		return p, err
	}
	p.profiles, err = windows.KnownFolderPath(windows.FOLDERID_UserProfiles, windows.KF_FLAG_DONT_VERIFY)
	if err != nil {
		return p, err
	}
	for _, id := range []*windows.KNOWNFOLDERID{windows.FOLDERID_Windows, windows.FOLDERID_ProgramFiles, windows.FOLDERID_ProgramFilesX86, windows.FOLDERID_ProgramData} {
		path, err := windows.KnownFolderPath(id, windows.KF_FLAG_DONT_VERIFY)
		if err != nil {
			return p, err
		}
		p.system = append(p.system, path)
	}
	return p, nil
}

func (p secretDirPolicy) check(path string) error {
	path = filepath.Clean(path)
	if filepath.Dir(path) == path || strings.EqualFold(path, filepath.Clean(p.profile)) ||
		strings.EqualFold(path, filepath.Clean(p.profiles)) || strings.EqualFold(filepath.Dir(path), filepath.Clean(p.profiles)) {
		return fmt.Errorf("refusing secrets directory at drive or profile root: %s", path)
	}
	for _, system := range p.system {
		system = filepath.Clean(system)
		if strings.EqualFold(path, system) || strings.HasPrefix(strings.ToLower(path), strings.ToLower(system)+`\`) {
			return fmt.Errorf("refusing secrets directory in system folder: %s", path)
		}
	}
	return nil
}
