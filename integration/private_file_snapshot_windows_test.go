//go:build windows

package integration

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"unsafe"

	"github.com/fr3akX/artisan-cli/internal/securefile"
	"golang.org/x/sys/windows"
)

// windowsPrivateACE stores canonical ACE values so comparisons never depend on
// security-descriptor allocation addresses or serialized pointer-bearing data.
type windowsPrivateACE struct {
	aceType uint8
	flags   uint8
	size    uint16
	mask    windows.ACCESS_MASK
	sid     string
}

type windowsPrivateSecurityState struct {
	control            windows.SECURITY_DESCRIPTOR_CONTROL
	descriptorRevision uint32
	daclDefaulted      bool
	aces               []windowsPrivateACE
}

type privateFileSnapshot struct {
	contents []byte
	info     os.FileInfo
	mode     os.FileMode
	security windowsPrivateSecurityState
}

func snapshotPrivateFile(path string) (privateFileSnapshot, error) {
	info, security, contents, err := readWindowsPrivateSnapshotState(path)
	if err != nil {
		return privateFileSnapshot{}, err
	}
	if err := verifyWindowsPrivateFile(path); err != nil {
		return privateFileSnapshot{}, err
	}
	return privateFileSnapshot{contents: contents, info: info, mode: info.Mode(), security: security}, nil
}

func privateFileMatchesSnapshot(path string, snapshot privateFileSnapshot) error {
	info, security, contents, err := readWindowsPrivateSnapshotState(path)
	if err != nil {
		return err
	}
	if !os.SameFile(snapshot.info, info) {
		return errors.New("download file identity changed")
	}
	if info.Mode() != snapshot.mode {
		return fmt.Errorf("download mode changed from %s to %s", snapshot.mode, info.Mode())
	}
	if err := compareWindowsPrivateSecurity(snapshot.security, security); err != nil {
		return err
	}
	if err := verifyWindowsPrivateFile(path); err != nil {
		return err
	}
	if !bytes.Equal(contents, snapshot.contents) {
		return errors.New("download contents changed")
	}
	return nil
}

func readWindowsPrivateSnapshotState(path string) (os.FileInfo, windowsPrivateSecurityState, []byte, error) {
	namedInfo, err := os.Lstat(path)
	if err != nil {
		return nil, windowsPrivateSecurityState{}, nil, fmt.Errorf("download could not be inspected: %w", err)
	}
	if !namedInfo.Mode().IsRegular() {
		return nil, windowsPrivateSecurityState{}, nil, errors.New("download is not a regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, windowsPrivateSecurityState{}, nil, fmt.Errorf("download exact file could not be opened: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, windowsPrivateSecurityState{}, nil, fmt.Errorf("download exact file could not be inspected: %w", err)
	}
	if !info.Mode().IsRegular() || !os.SameFile(namedInfo, info) {
		return nil, windowsPrivateSecurityState{}, nil, errors.New("download path did not open the inspected regular file")
	}
	security, err := readWindowsPrivateSecurity(file)
	if err != nil {
		return nil, windowsPrivateSecurityState{}, nil, err
	}
	contents, err := io.ReadAll(file)
	if err != nil {
		return nil, windowsPrivateSecurityState{}, nil, errors.New("download exact file contents could not be read")
	}
	return info, security, contents, nil
}

func readWindowsPrivateSecurity(file *os.File) (windowsPrivateSecurityState, error) {
	descriptor, err := windows.GetSecurityInfo(windows.Handle(file.Fd()), windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return windowsPrivateSecurityState{}, fmt.Errorf("download exact-file DACL could not be read: %w", err)
	}
	control, descriptorRevision, err := descriptor.Control()
	if err != nil {
		return windowsPrivateSecurityState{}, fmt.Errorf("download DACL control could not be read: %w", err)
	}
	dacl, defaulted, err := descriptor.DACL()
	if err != nil {
		return windowsPrivateSecurityState{}, fmt.Errorf("download DACL could not be extracted: %w", err)
	}
	if dacl == nil {
		return windowsPrivateSecurityState{}, errors.New("download has a nil unrestricted DACL")
	}
	state := windowsPrivateSecurityState{
		control:            control,
		descriptorRevision: descriptorRevision,
		daclDefaulted:      defaulted,
		aces:               make([]windowsPrivateACE, 0, dacl.AceCount),
	}
	for index := uint16(0); index < dacl.AceCount; index++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, uint32(index), &ace); err != nil {
			return windowsPrivateSecurityState{}, fmt.Errorf("download DACL ACE %d could not be read: %w", index, err)
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		state.aces = append(state.aces, windowsPrivateACE{
			aceType: ace.Header.AceType,
			flags:   ace.Header.AceFlags,
			size:    ace.Header.AceSize,
			mask:    ace.Mask,
			sid:     sid.String(),
		})
	}
	runtime.KeepAlive(descriptor)
	return state, nil
}

func compareWindowsPrivateSecurity(want, got windowsPrivateSecurityState) error {
	if got.control != want.control {
		return fmt.Errorf("download DACL control changed from %#x to %#x", want.control, got.control)
	}
	if got.descriptorRevision != want.descriptorRevision {
		return fmt.Errorf("download security descriptor revision changed from %d to %d", want.descriptorRevision, got.descriptorRevision)
	}
	if got.daclDefaulted != want.daclDefaulted {
		return fmt.Errorf("download DACL defaulted state changed from %t to %t", want.daclDefaulted, got.daclDefaulted)
	}
	if len(got.aces) != len(want.aces) {
		return fmt.Errorf("download DACL ACE count changed from %d to %d", len(want.aces), len(got.aces))
	}
	for index := range want.aces {
		switch {
		case got.aces[index].aceType != want.aces[index].aceType:
			return fmt.Errorf("download DACL ACE %d type changed from %#x to %#x", index, want.aces[index].aceType, got.aces[index].aceType)
		case got.aces[index].flags != want.aces[index].flags:
			return fmt.Errorf("download DACL ACE %d flags changed from %#x to %#x", index, want.aces[index].flags, got.aces[index].flags)
		case got.aces[index].size != want.aces[index].size:
			return fmt.Errorf("download DACL ACE %d size changed from %d to %d", index, want.aces[index].size, got.aces[index].size)
		case got.aces[index].mask != want.aces[index].mask:
			return fmt.Errorf("download DACL ACE %d mask changed from %#x to %#x", index, want.aces[index].mask, got.aces[index].mask)
		case got.aces[index].sid != want.aces[index].sid:
			return fmt.Errorf("download DACL ACE %d SID changed from %s to %s", index, want.aces[index].sid, got.aces[index].sid)
		}
	}
	return nil
}

func verifyWindowsPrivateFile(path string) error {
	file, err := securefile.OpenPrivate(path)
	if err != nil {
		return fmt.Errorf("download does not retain a valid private protected DACL: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("download private verification handle could not be closed: %w", err)
	}
	return nil
}

func TestWindowsPrivateSecurityComparatorDetectsCanonicalChanges(t *testing.T) {
	original := windowsPrivateSecurityState{
		control:            windows.SE_DACL_PRESENT | windows.SE_DACL_PROTECTED | windows.SE_SELF_RELATIVE,
		descriptorRevision: 1,
		daclDefaulted:      false,
		aces: []windowsPrivateACE{{
			aceType: windows.ACCESS_ALLOWED_ACE_TYPE,
			flags:   0,
			size:    24,
			mask:    windows.GENERIC_ALL,
			sid:     "S-1-5-21-1",
		}},
	}
	if err := compareWindowsPrivateSecurity(original, original); err != nil {
		t.Fatalf("identical canonical Windows security state rejected: %v", err)
	}
	tests := []struct {
		name   string
		mutate func(*windowsPrivateSecurityState)
	}{
		{"control", func(state *windowsPrivateSecurityState) { state.control &^= windows.SE_DACL_PROTECTED }},
		{"descriptor revision", func(state *windowsPrivateSecurityState) { state.descriptorRevision++ }},
		{"defaulted", func(state *windowsPrivateSecurityState) { state.daclDefaulted = true }},
		{"ACE count", func(state *windowsPrivateSecurityState) { state.aces = append(state.aces, state.aces[0]) }},
		{"ACE type", func(state *windowsPrivateSecurityState) { state.aces[0].aceType = windows.ACCESS_DENIED_ACE_TYPE }},
		{"ACE flags", func(state *windowsPrivateSecurityState) { state.aces[0].flags = windows.INHERITED_ACE }},
		{"ACE size", func(state *windowsPrivateSecurityState) { state.aces[0].size++ }},
		{"ACE mask", func(state *windowsPrivateSecurityState) { state.aces[0].mask = windows.GENERIC_READ }},
		{"ACE SID", func(state *windowsPrivateSecurityState) { state.aces[0].sid = "S-1-1-0" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			changed := original
			changed.aces = append([]windowsPrivateACE(nil), original.aces...)
			test.mutate(&changed)
			if err := compareWindowsPrivateSecurity(original, changed); err == nil {
				t.Fatal("changed canonical Windows security state was accepted")
			}
		})
	}
}

func TestPrivateFileSnapshotDetectsWindowsDACLChanges(t *testing.T) {
	tests := []struct {
		name      string
		wantError string
		mutate    func(*testing.T, string)
	}{
		{
			name:      "control",
			wantError: "DACL control changed",
			mutate:    unprotectWindowsTestDACL,
		},
		{
			name:      "SID",
			wantError: "DACL ACE 1 SID changed",
			mutate: func(t *testing.T, path string) {
				setWindowsTestDACL(t, path, "(A;;GA;;;WD)")
			},
		},
		{
			name:      "mask",
			wantError: "DACL ACE 1 mask changed",
			mutate: func(t *testing.T, path string) {
				setWindowsTestDACL(t, path, "(A;;GR;;;SY)")
			},
		},
		{
			name:      "flags",
			wantError: "DACL ACE 1 flags changed",
			mutate: func(t *testing.T, path string) {
				setWindowsTestDACL(t, path, "(A;IO;GA;;;SY)")
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			const name = "download"
			path := filepath.Join(dir, name)
			if err := securefile.AtomicWrite(dir, name, []byte("private download")); err != nil {
				if runningUnderWine() {
					t.Skipf("Wine does not preserve protected Windows DACLs: %v", err)
				}
				t.Fatalf("create private test file: %v", err)
			}
			snapshot, err := snapshotPrivateFile(path)
			if err != nil {
				t.Fatalf("snapshot private test file: %v", err)
			}

			test.mutate(t, path)
			err = privateFileMatchesSnapshot(path, snapshot)
			if err == nil {
				t.Fatal("changed Windows DACL was accepted")
			}
			if !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("changed Windows DACL error = %q, want %q", err, test.wantError)
			}
		})
	}
}

func unprotectWindowsTestDACL(t *testing.T, path string) {
	t.Helper()
	descriptor, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatalf("read private test DACL: %v", err)
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		t.Fatalf("extract private test DACL: %v", err)
	}
	if err := windows.SetNamedSecurityInfo(
		path,
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.UNPROTECTED_DACL_SECURITY_INFORMATION,
		nil,
		nil,
		dacl,
		nil,
	); err != nil {
		t.Fatalf("unprotect private test DACL: %v", err)
	}
	runtime.KeepAlive(descriptor)
}

func setWindowsTestDACL(t *testing.T, path, systemACE string) {
	t.Helper()
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatalf("get current user SID: %v", err)
	}
	descriptor, err := windows.SecurityDescriptorFromString(
		fmt.Sprintf("D:P(A;;GA;;;%s)%s(A;;GA;;;BA)", user.User.Sid.String(), systemACE),
	)
	if err != nil {
		t.Fatalf("build mutated private test security descriptor: %v", err)
	}
	acl, _, err := descriptor.DACL()
	if err != nil {
		t.Fatalf("extract mutated private test DACL: %v", err)
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
		t.Fatalf("apply mutated private test DACL: %v", err)
	}
	runtime.KeepAlive(descriptor)
	runtime.KeepAlive(user)
}

func runningUnderWine() bool {
	return windows.NewLazySystemDLL("ntdll.dll").NewProc("wine_get_version").Find() == nil
}

func TestPrivateFileSnapshotDetectsNoClobberChanges(t *testing.T) {
	dir := t.TempDir()
	const name = "download"
	path := filepath.Join(dir, name)
	original := []byte("private download")
	if err := securefile.AtomicWrite(dir, name, original); err != nil {
		if runningUnderWine() {
			t.Skipf("Wine does not preserve protected Windows DACLs: %v", err)
		}
		t.Fatalf("create private test file: %v", err)
	}
	snapshot, err := snapshotPrivateFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := privateFileMatchesSnapshot(path, snapshot); err != nil {
		t.Fatalf("unchanged file rejected: %v", err)
	}

	if err := os.WriteFile(path, []byte("changed download"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := privateFileMatchesSnapshot(path, snapshot); err == nil {
		t.Fatal("changed bytes were accepted")
	}
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	moved := path + ".moved"
	if err := os.Rename(path, moved); err != nil {
		t.Fatal(err)
	}
	if err := securefile.AtomicWrite(dir, name, original); err != nil {
		t.Fatalf("create replacement private test file: %v", err)
	}
	if err := privateFileMatchesSnapshot(path, snapshot); err == nil {
		t.Fatal("replacement file identity was accepted")
	}
}
