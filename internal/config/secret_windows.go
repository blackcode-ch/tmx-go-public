//go:build windows

package config

import (
	"errors"
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

// A secret on Windows gets a protected DACL (nothing inherited from the folder) that gives full
// control to the current user and SYSTEM only, and the current user owns it. SYSTEM stays because
// services such as backup and antivirus run as it and it can read any file anyway.
// Administrators are left out, like everyone but the owner with 0600: an administrator can still
// take ownership, but has to do it on purpose, and the user's own elevated processes get in as
// the user. A secrets folder has the same two entries, inherited by what is made in it (OICI),
// so files tmx does not write itself, such as whatsapp.db's -wal and -shm, are private too.

// secretSD is the security descriptor for a new secret file, or for a secrets folder (dir).
func secretSD(dir bool) (*windows.SECURITY_DESCRIPTOR, *windows.SID, error) {
	u, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, nil, err
	}
	sid := u.User.Sid.String()
	if sid == "" {
		return nil, nil, errors.New("cannot read the current user's SID")
	}
	inherit := ""
	if dir {
		inherit = "OICI"
	}
	sd, err := windows.SecurityDescriptorFromString(
		fmt.Sprintf("O:%sD:P(A;%s;FA;;;%s)(A;%s;FA;;;SY)", sid, inherit, sid, inherit))
	return sd, u.User.Sid, err
}

func securityAttributes(sd *windows.SECURITY_DESCRIPTOR) *windows.SecurityAttributes {
	return &windows.SecurityAttributes{
		Length:             uint32(unsafe.Sizeof(windows.SecurityAttributes{})),
		SecurityDescriptor: sd,
	}
}

func createSecret(path string) (*os.File, error) {
	if err := testModePath(path); err != nil {
		return nil, err
	}
	sd, user, err := secretSD(false)
	if err != nil {
		return nil, err
	}
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	// The DACL is set as the file is created, so no other user can open it even for a moment.
	// No FILE_SHARE_DELETE: nobody can rename or delete it while it is open here.
	h, err := windows.CreateFile(name, windows.GENERIC_WRITE|windows.READ_CONTROL,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, securityAttributes(sd),
		windows.CREATE_NEW, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return nil, &os.PathError{Op: "create", Path: path, Err: err}
	}
	f := os.NewFile(uintptr(h), path)
	// A file system without ACLs (FAT, some network shares) ignores the DACL. No file there can
	// be private, so the secret is not written at all.
	got, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil || !isPrivate(got, user) {
		f.Close()
		os.Remove(path)
		return nil, fmt.Errorf("%s: this file system cannot keep a file private to the current user", path)
	}
	return f, nil
}

// isPrivate reports whether sd's DACL is protected and only lets in user and SYSTEM, user included.
func isPrivate(sd *windows.SECURITY_DESCRIPTOR, user *windows.SID) bool {
	if sd == nil {
		return false
	}
	ctl, _, err := sd.Control()
	if err != nil || ctl&windows.SE_DACL_PROTECTED == 0 {
		return false
	}
	dacl, _, err := sd.DACL()
	if err != nil || dacl == nil { // a NULL DACL lets everyone in
		return false
	}
	hasUser := false
	for i := range uint32(dacl.AceCount) {
		var ace *windows.ACCESS_ALLOWED_ACE
		if windows.GetAce(dacl, i, &ace) != nil || ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			return false
		}
		switch sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart)); {
		case sid.Equals(user):
			hasUser = true
		case !sid.IsWellKnown(windows.WinLocalSystemSid):
			return false
		}
	}
	return hasUser
}
