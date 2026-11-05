//go:build aclmutate

package config

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

const systemSID = "S-1-5-18"

// Companion's receiving path uses WriteSecret at this exact state-directory
// location; both first delivery and rotation must have protected user-only ACLs
// (SYSTEM is the existing privileged exception).
func TestCompanionAgentKeyDACL(t *testing.T) {
	p := Paths{Home: t.TempDir()}
	for _, value := range []string{"first-test-credential", "rotated-test-credential"} {
		if err := WriteSecret(p.CompanionAgentTokenFile(), value); err != nil {
			t.Fatal(err)
		}
		assertPrivate(t, p.CompanionAgentTokenFile(), true, true)
	}
}

func currentUserSID(t *testing.T) string {
	t.Helper()
	u, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	return u.User.Sid.String()
}

// aclOf returns whether path's DACL is protected, the SIDs of its allow entries (sorted; any
// other kind of entry shows as "ace-type-N") and its owner.
func aclOf(t *testing.T, path string) (protected bool, sids []string, owner string) {
	t.Helper()
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil || sd == nil {
		t.Fatalf("%s: security info: %v", path, err)
	}
	ctl, _, err := sd.Control()
	if err != nil {
		t.Fatal(err)
	}
	o, _, err := sd.Owner()
	if err != nil {
		t.Fatal(err)
	}
	dacl, _, err := sd.DACL()
	if err != nil || dacl == nil {
		t.Fatalf("%s: no DACL (%v)", path, err)
	}
	for i := range uint32(dacl.AceCount) {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, i, &ace); err != nil {
			t.Fatal(err)
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			sids = append(sids, fmt.Sprintf("ace-type-%d", ace.Header.AceType))
			continue
		}
		sids = append(sids, (*windows.SID)(unsafe.Pointer(&ace.SidStart)).String())
	}
	slices.Sort(sids)
	sids = slices.Compact(sids)
	return ctl&windows.SE_DACL_PROTECTED != 0, sids, o.String()
}

// assertPrivate checks that only the user and SYSTEM get in, that the DACL is protected (not
// for files that inherit theirs) and that the user owns it (not for what tmx did not create).
func assertPrivate(t *testing.T, path string, wantProtected, wantOwner bool) {
	t.Helper()
	user := currentUserSID(t)
	protected, sids, owner := aclOf(t, path)
	want := []string{user, systemSID}
	slices.Sort(want)
	if !slices.Equal(sids, want) {
		t.Fatalf("%s: DACL lets in %v, want only the user and SYSTEM %v", path, sids, want)
	}
	if wantProtected && !protected {
		t.Fatalf("%s: DACL is not protected", path)
	}
	if wantOwner && owner != user {
		t.Fatalf("%s: owner %s, want the user %s", path, owner, user)
	}
}

// setDACL gives path an SDDL DACL, as an older version or a shared folder might have.
func setDACL(t *testing.T, path, sddl string) {
	t.Helper()
	if err := testModePath(path); err != nil {
		t.Fatal(err)
	}
	t.Helper()
	sd, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		t.Fatal(err)
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.UNPROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil); err != nil {
		t.Fatal(err)
	}
}

func TestSecretFileDACL(t *testing.T) {
	p := Paths{Home: t.TempDir()}
	if err := WriteSecretFile(p.GitHubTokenFile(), []byte("tok\n")); err != nil {
		t.Fatal(err)
	}
	assertPrivate(t, p.SecretsDir(), true, true) // made by WriteSecretFile
	assertPrivate(t, p.GitHubTokenFile(), true, true)
	if s, err := ReadSecret(p.GitHubTokenFile()); err != nil || s != "tok" {
		t.Fatalf("read back %q, %v", s, err)
	}
	f, err := CreateSecretFile(p.OwnerTokenFile())
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	assertPrivate(t, p.OwnerTokenFile(), true, true)
	// What others make in the folder (SQLite's -wal) inherits the folder's two entries.
	other := filepath.Join(p.SecretsDir(), "whatsapp.db-wal")
	if err := os.WriteFile(other, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	assertPrivate(t, other, false, false)
}

// A secret written over a file that inherited a wide ACL, in a folder anyone can use, ends up
// private: the rename keeps the temp file's own DACL.
func TestSecretReplaceInWideFolder(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "shared")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	setDACL(t, dir, "D:(A;OICI;FA;;;WD)") // Everyone, inherited by new files
	path := filepath.Join(dir, "gate.json")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, sids, _ := aclOf(t, path); !slices.Contains(sids, "S-1-1-0") {
		t.Fatalf("setup: the old file should let Everyone in: %v", sids)
	}
	if err := WriteSecretFile(path, []byte("new")); err != nil {
		t.Fatal(err)
	}
	assertPrivate(t, path, true, true)
	if b, _ := os.ReadFile(path); string(b) != "new" {
		t.Fatalf("read back %q", b)
	}
	if _, sids, _ := aclOf(t, dir); !slices.Contains(sids, "S-1-1-0") {
		t.Fatal("WriteSecretFile must leave an existing folder's ACL alone")
	}
}
