//go:build windows

package auth

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"unsafe"

	"github.com/fr3akX/artisan-cli/internal/securefile"
	"golang.org/x/sys/windows"
)

func TestFileStoreCreatesAndVerifiesPrivateWindowsACL(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "artisan")
	store := NewFileStore(dir)
	if err := store.Save("secret-token"); err != nil {
		t.Fatalf("Save: %v", err)
	}
	path := filepath.Join(dir, credentialsFileName)
	file, err := securefile.OpenPrivate(path)
	if err != nil {
		t.Fatalf("OpenPrivate: %v", err)
	}
	file.Close()

	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatalf("GetNamedSecurityInfo: %v", err)
	}
	control, _, err := sd.Control()
	if err != nil {
		t.Fatalf("security descriptor Control: %v", err)
	}
	if control&windows.SE_DACL_PROTECTED == 0 {
		t.Fatal("credential DACL inherits permissions")
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		t.Fatalf("security descriptor DACL: %v", err)
	}
	if dacl == nil {
		t.Fatal("credential has a nil (unrestricted) DACL")
	}

	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatalf("GetTokenUser: %v", err)
	}
	allowed := map[string]bool{user.User.Sid.String(): true}
	for _, sidType := range []windows.WELL_KNOWN_SID_TYPE{
		windows.WinLocalSystemSid,
		windows.WinBuiltinAdministratorsSid,
	} {
		sid, err := windows.CreateWellKnownSid(sidType)
		if err != nil {
			t.Fatalf("CreateWellKnownSid(%d): %v", sidType, err)
		}
		allowed[sid.String()] = true
	}

	for i := uint16(0); i < dacl.AceCount; i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, uint32(i), &ace); err != nil {
			t.Fatalf("GetAce(%d): %v", i, err)
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			continue
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if !allowed[sid.String()] {
			t.Errorf("DACL grants access to unexpected SID %s", sid)
		}
	}
}

func TestFileStoreRejectsWindowsACLGrantingEveryone(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, credentialsFileName)
	if err := os.WriteFile(path, []byte(`{"token":"secret"}`), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	world, err := windows.CreateWellKnownSid(windows.WinWorldSid)
	if err != nil {
		t.Fatalf("CreateWellKnownSid: %v", err)
	}
	acl, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{{
		AccessPermissions: windows.GENERIC_READ,
		AccessMode:        windows.GRANT_ACCESS,
		Trustee: windows.TRUSTEE{
			TrusteeForm:  windows.TRUSTEE_IS_SID,
			TrusteeType:  windows.TRUSTEE_IS_WELL_KNOWN_GROUP,
			TrusteeValue: windows.TrusteeValueFromSID(world),
		},
	}}, nil)
	runtime.KeepAlive(world)
	if err != nil {
		t.Fatalf("ACLFromEntries: %v", err)
	}
	if err := windows.SetNamedSecurityInfo(
		path,
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil,
		nil,
		acl,
		nil,
	); err != nil {
		t.Fatalf("SetNamedSecurityInfo: %v", err)
	}

	if _, err := NewFileStore(dir).Load(); err == nil {
		t.Fatal("Load succeeded with an Everyone-readable credential DACL")
	}
}
