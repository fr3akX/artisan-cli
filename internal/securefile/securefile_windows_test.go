//go:build windows

package securefile_test

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/fr3akX/artisan-cli/internal/securefile"
	"golang.org/x/sys/windows"
)

func TestPrivateWindowsACLsForDirectoryAndFile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "artisan")
	if err := securefile.AtomicWrite(dir, "config.json", []byte("private")); err != nil {
		t.Fatalf("AtomicWrite: %v", err)
	}
	file, err := securefile.OpenPrivate(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatalf("OpenPrivate: %v", err)
	}
	file.Close()
}

func TestOpenPrivateRejectsWindowsInheritOnlyFileACE(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := securefile.AtomicWrite(dir, "config.json", []byte("private")); err != nil {
		t.Fatalf("AtomicWrite: %v", err)
	}
	acl := requiredWindowsACL(t, windows.INHERIT_ONLY_ACE)
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil, nil, acl, nil); err != nil {
		t.Fatalf("SetNamedSecurityInfo: %v", err)
	}
	if file, err := securefile.OpenPrivate(path); err == nil {
		file.Close()
		t.Fatal("OpenPrivate accepted INHERIT_ONLY file ACE")
	}
}

func TestOpenPrivateRejectsWindowsReparsePoint(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := securefile.AtomicWrite(dir, "target", []byte("private")); err != nil {
		t.Fatalf("AtomicWrite target: %v", err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if file, err := securefile.OpenPrivate(link); err == nil {
		file.Close()
		t.Fatal("OpenPrivate followed a reparse point")
	}
}

func requiredWindowsACL(t *testing.T, fileFlags uint32) *windows.ACL {
	t.Helper()
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatalf("GetTokenUser: %v", err)
	}
	system, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		t.Fatalf("Create SYSTEM SID: %v", err)
	}
	administrators, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		t.Fatalf("Create Administrators SID: %v", err)
	}
	makeEntry := func(sid *windows.SID, trusteeType windows.TRUSTEE_TYPE, flags uint32) windows.EXPLICIT_ACCESS {
		return windows.EXPLICIT_ACCESS{
			AccessPermissions: windows.GENERIC_ALL,
			AccessMode:        windows.GRANT_ACCESS,
			Inheritance:       flags,
			Trustee: windows.TRUSTEE{
				TrusteeForm: windows.TRUSTEE_IS_SID, TrusteeType: trusteeType,
				TrusteeValue: windows.TrusteeValueFromSID(sid),
			},
		}
	}
	entries := []windows.EXPLICIT_ACCESS{
		makeEntry(user.User.Sid, windows.TRUSTEE_IS_USER, fileFlags),
		makeEntry(system, windows.TRUSTEE_IS_USER, 0),
		makeEntry(administrators, windows.TRUSTEE_IS_GROUP, 0),
	}
	acl, err := windows.ACLFromEntries(entries, nil)
	runtime.KeepAlive(user)
	runtime.KeepAlive(system)
	runtime.KeepAlive(administrators)
	if err != nil {
		t.Fatalf("ACLFromEntries: %v", err)
	}
	return acl
}
