//go:build windows

package securefile

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestProtectPrivateUsesIdentityCheckedCandidateForOrdinaryHandles(t *testing.T) {
	directoryPath := filepath.Join(t.TempDir(), "private-directory")
	if err := os.Mkdir(directoryPath, 0o700); err != nil {
		t.Fatal(err)
	}
	directory, err := os.Open(directoryPath)
	if err != nil {
		t.Fatal(err)
	}
	defer directory.Close()
	if err := protectPrivate(directory, true); err != nil {
		t.Fatalf("protect ordinary directory handle: %v", err)
	}
	if err := verifyPrivateHandle(windows.Handle(directory.Fd()), true); err != nil {
		t.Fatalf("verify protected ordinary directory handle: %v", err)
	}

	filePath := filepath.Join(directoryPath, "private-file")
	file, err := os.OpenFile(filePath, os.O_CREATE|os.O_RDWR|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := protectPrivate(file, false); err != nil {
		t.Fatalf("protect ordinary file handle: %v", err)
	}
	if err := verifyPrivateHandle(windows.Handle(file.Fd()), false); err != nil {
		t.Fatalf("verify protected ordinary file handle: %v", err)
	}
}

func TestProtectPrivateFinalPathFollowsRenamedHeldObject(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "private-file")
	heldPath := filepath.Join(root, "held-private-file")
	if err := os.WriteFile(path, []byte("held"), 0o600); err != nil {
		t.Fatal(err)
	}
	pathPointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := windows.CreateFile(pathPointer, windows.GENERIC_READ|windows.READ_CONTROL,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil,
		windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		t.Fatal(err)
	}
	file := os.NewFile(uintptr(handle), path)
	defer file.Close()
	if err := os.Rename(path, heldPath); err != nil {
		t.Fatalf("rename opened fixture: %v", err)
	}
	if err := os.WriteFile(path, []byte("replacement"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := protectPrivate(file, false); err != nil {
		t.Fatalf("protect renamed opened object: %v", err)
	}
	if err := verifyPrivateHandle(windows.Handle(file.Fd()), false); err != nil {
		t.Fatalf("verify renamed opened object: %v", err)
	}
	replacement, err := openWindowsObject(path, false, false)
	if err != nil {
		t.Fatal(err)
	}
	defer replacement.Close()
	if err := verifyPrivateHandle(windows.Handle(replacement.Fd()), false); err == nil {
		t.Fatal("stale path replacement received the held object's private DACL")
	}
}

func TestProtectPrivateRejectsFinalPathReplacementBeforeDACLMutation(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "private-file")
	heldPath := filepath.Join(root, "held-private-file")
	if err := os.WriteFile(path, []byte("held"), 0o600); err != nil {
		t.Fatal(err)
	}
	pathPointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := windows.CreateFile(pathPointer, windows.GENERIC_READ|windows.READ_CONTROL,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil,
		windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		t.Fatal(err)
	}
	file := os.NewFile(uintptr(handle), path)
	defer file.Close()
	if err := verifyPrivateHandle(windows.Handle(file.Fd()), false); err == nil {
		t.Fatal("race fixture unexpectedly begins with a private DACL")
	}

	err = protectPrivateWithHooks(file, false, privateProtectionHooks{afterFinalPath: func() error {
		if err := os.Rename(path, heldPath); err != nil {
			return err
		}
		return os.WriteFile(path, []byte("replacement"), 0o600)
	}})
	if err == nil {
		t.Fatal("protectPrivate accepted a replacement at the handle-derived final path")
	}
	for _, forbidden := range []string{root, path, heldPath, "held", "replacement"} {
		if strings.Contains(err.Error(), forbidden) {
			t.Fatalf("privacy-safe race error %q contains %q", err, forbidden)
		}
	}
	if err := verifyPrivateHandle(windows.Handle(file.Fd()), false); err == nil {
		t.Fatal("held object DACL changed after candidate identity mismatch")
	}
	replacement, openErr := openWindowsObject(path, false, false)
	if openErr != nil {
		t.Fatal(openErr)
	}
	defer replacement.Close()
	if err := verifyPrivateHandle(windows.Handle(replacement.Fd()), false); err == nil {
		t.Fatal("replacement DACL changed before candidate identity matched")
	}
}

func TestAppliedPrivateACLNativeNormalization(t *testing.T) {
	tests := []struct {
		name      string
		directory bool
	}{
		{name: "directory", directory: true},
		{name: "file", directory: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := t.TempDir()
			if !test.directory {
				path = filepath.Join(path, "private-file")
				if err := os.WriteFile(path, []byte("private"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			descriptor, acl, err := privateACL(test.directory)
			if err != nil {
				t.Fatal(err)
			}
			err = windows.SetNamedSecurityInfo(
				path,
				windows.SE_FILE_OBJECT,
				windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
				nil,
				nil,
				acl,
				nil,
			)
			runtime.KeepAlive(descriptor)
			if err != nil {
				t.Fatalf("apply private %s DACL directly: %v", test.name, err)
			}

			appliedDescriptor, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
			if err != nil {
				t.Fatalf("read applied private %s DACL: %v", test.name, err)
			}
			control, _, err := appliedDescriptor.Control()
			if err != nil {
				t.Fatalf("read applied private %s DACL control: %v", test.name, err)
			}
			appliedACL, _, err := appliedDescriptor.DACL()
			if err != nil {
				t.Fatalf("extract applied private %s DACL: control=%#x: %v", test.name, control, err)
			}
			if appliedACL == nil {
				t.Fatalf("applied private %s DACL mismatch: control=%#x aceCount=0 applied=[]: DACL is absent", test.name, control)
			}

			type appliedACE struct {
				Index    uint16
				AceType  uint8
				AceFlags uint8
				Mask     windows.ACCESS_MASK
				SID      string
			}
			applied := make([]appliedACE, 0, appliedACL.AceCount)
			for i := uint16(0); i < appliedACL.AceCount; i++ {
				var ace *windows.ACCESS_ALLOWED_ACE
				if err := windows.GetAce(appliedACL, uint32(i), &ace); err != nil {
					t.Fatalf("enumerate applied private %s DACL: control=%#x aceCount=%d applied=%+v GetAce(%d): %v", test.name, control, appliedACL.AceCount, applied, i, err)
				}
				sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
				applied = append(applied, appliedACE{
					Index:    i,
					AceType:  ace.Header.AceType,
					AceFlags: ace.Header.AceFlags,
					Mask:     ace.Mask,
					SID:      sid.String(),
				})
			}
			runtime.KeepAlive(appliedDescriptor)

			user, err := windows.GetCurrentProcessToken().GetTokenUser()
			if err != nil {
				t.Fatal(err)
			}
			system, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
			if err != nil {
				t.Fatal(err)
			}
			administrators, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
			if err != nil {
				t.Fatal(err)
			}
			wantSIDs := []string{user.User.Sid.String(), system.String(), administrators.String()}
			effectiveSeen := make(map[string]int, len(wantSIDs))
			propagationSeen := make(map[string]int, len(wantSIDs))
			wantCount := len(wantSIDs)
			if test.directory {
				wantCount *= 2
			}
			matches := control&windows.SE_DACL_PROTECTED != 0 && len(applied) == wantCount
			propagationFlags := uint8(windows.OBJECT_INHERIT_ACE | windows.CONTAINER_INHERIT_ACE | windows.INHERIT_ONLY_ACE)
			allowed := make(map[string]bool, len(wantSIDs))
			for _, sid := range wantSIDs {
				allowed[sid] = true
			}
			for _, ace := range applied {
				if ace.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || !allowed[ace.SID] {
					matches = false
					continue
				}
				switch {
				case ace.AceFlags == 0 && ace.Mask == windowsFileAllAccess:
					effectiveSeen[ace.SID]++
				case test.directory && ace.AceFlags == propagationFlags && ace.Mask == windows.GENERIC_ALL:
					propagationSeen[ace.SID]++
				default:
					matches = false
				}
			}
			for _, sid := range wantSIDs {
				if effectiveSeen[sid] != 1 || (test.directory && propagationSeen[sid] != 1) {
					matches = false
				}
			}
			if !matches {
				t.Fatalf("applied private %s DACL mismatch: control=%#x aceCount=%d applied=%+v wantProtected=%#x wantACECount=%d wantType=%#x wantEffectiveFlags=0 wantEffectiveMask=%#x wantPropagationFlags=%#x wantPropagationMask=%#x wantSIDs=%v", test.name, control, appliedACL.AceCount, applied, windows.SE_DACL_PROTECTED, wantCount, windows.ACCESS_ALLOWED_ACE_TYPE, windowsFileAllAccess, propagationFlags, windows.GENERIC_ALL, wantSIDs)
			}
		})
	}
}

func TestPrivateACLACEFlags(t *testing.T) {
	tests := []struct {
		name      string
		directory bool
		wantFlags uint8
	}{
		{
			name:      "directory",
			directory: true,
			wantFlags: windows.OBJECT_INHERIT_ACE | windows.CONTAINER_INHERIT_ACE,
		},
		{
			name:      "file",
			directory: false,
			wantFlags: 0,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			descriptor, acl, err := privateACL(test.directory)
			if err != nil {
				t.Fatalf("privateACL(%t): %v", test.directory, err)
			}
			if acl.AceCount != 3 {
				t.Fatalf("privateACL(%t) ACE count = %d, want 3", test.directory, acl.AceCount)
			}
			for i := uint16(0); i < acl.AceCount; i++ {
				var ace *windows.ACCESS_ALLOWED_ACE
				if err := windows.GetAce(acl, uint32(i), &ace); err != nil {
					t.Fatalf("privateACL(%t) GetAce(%d): %v", test.directory, i, err)
				}
				if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
					t.Errorf("privateACL(%t) ACE %d type = %d, want %d", test.directory, i, ace.Header.AceType, windows.ACCESS_ALLOWED_ACE_TYPE)
				}
				if ace.Header.AceFlags != test.wantFlags {
					t.Errorf("privateACL(%t) ACE %d flags = %#x, want %#x", test.directory, i, ace.Header.AceFlags, test.wantFlags)
				}
			}
			runtime.KeepAlive(descriptor)
		})
	}
}
