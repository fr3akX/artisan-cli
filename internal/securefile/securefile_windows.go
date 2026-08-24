//go:build windows

package securefile

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var reOpenFileProc = windows.NewLazySystemDLL("kernel32.dll").NewProc("ReOpenFile")

// OpenPrivate opens a file without traversing a final reparse point and
// verifies the DACL on the exact handle used for subsequent reads.
func OpenPrivate(path string) (*os.File, error) {
	return openWindowsObject(path, false, true)
}

func durableReplace(from, to string) error {
	fromPointer, err := windows.UTF16PtrFromString(from)
	if err != nil {
		return err
	}
	toPointer, err := windows.UTF16PtrFromString(to)
	if err != nil {
		return err
	}
	return windows.MoveFileEx(fromPointer, toPointer, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
}

func durableRemove(dir, name string) error {
	path := filepath.Join(dir, name)
	// Windows has no parent-directory fsync equivalent for DeleteFile. A
	// write-through rename durably removes the canonical name; deletion of the
	// private tombstone is then cleanup-only because its resurrection cannot
	// restore the canonical file.
	tombstone := filepath.Join(dir, "."+name+".removed")
	if err := durableReplace(path, tombstone); err != nil {
		if !errors.Is(err, os.ErrNotExist) && !errors.Is(err, windows.ERROR_FILE_NOT_FOUND) && !errors.Is(err, windows.ERROR_PATH_NOT_FOUND) {
			return fmt.Errorf("remove private file: %w", err)
		}
	}
	if err := os.Remove(tombstone); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("clean private removal tombstone: %w", err)
	}
	return nil
}

// MoveFileEx with MOVEFILE_WRITE_THROUGH provides the Windows durability
// boundary corresponding to syncing the parent directory after a Unix rename.
func syncParentDirectory(string) error { return nil }

func openPrivateDirectory(path string) (*os.File, error) {
	return openWindowsObject(path, true, false)
}

func openWindowsObject(path string, directory, verify bool) (*os.File, error) {
	pathPointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, fmt.Errorf("encode private path: %w", err)
	}
	flags := uint32(windows.FILE_ATTRIBUTE_NORMAL | windows.FILE_FLAG_OPEN_REPARSE_POINT)
	if directory {
		flags |= windows.FILE_FLAG_BACKUP_SEMANTICS
	}
	handle, err := windows.CreateFile(
		pathPointer,
		windows.GENERIC_READ|windows.READ_CONTROL,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		flags,
		0,
	)
	if err != nil {
		return nil, &os.PathError{Op: "open private file", Path: path, Err: err}
	}

	var information windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &information); err != nil {
		windows.CloseHandle(handle)
		return nil, fmt.Errorf("inspect opened private file: %w", err)
	}
	if information.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		windows.CloseHandle(handle)
		return nil, errors.New("unsafe_private_file: reparse points are not allowed")
	}
	isDirectory := information.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0
	if isDirectory != directory {
		windows.CloseHandle(handle)
		if directory {
			return nil, errors.New("unsafe_private_file: expected a directory")
		}
		return nil, errors.New("unsafe_private_file: expected a regular file")
	}

	file := os.NewFile(uintptr(handle), path)
	if verify {
		if err := verifyPrivateHandle(handle, directory); err != nil {
			file.Close()
			return nil, err
		}
	}
	return file, nil
}

func protectPrivate(file *os.File, directory bool) error {
	// Ordinary os.File handles do not request WRITE_DAC. Reopen the existing
	// file object with the security rights needed to apply and verify its DACL;
	// never resolve file.Name(), which may now identify a replacement object.
	handle, err := reopenPrivateHandle(windows.Handle(file.Fd()), directory)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(handle)
	if err := applyPrivateACLHandle(handle, directory); err != nil {
		return err
	}
	return verifyPrivateHandle(handle, directory)
}

func reopenPrivateHandle(handle windows.Handle, directory bool) (windows.Handle, error) {
	access, share, flags := privateReopenParameters(directory)
	result, _, callErr := reOpenFileProc.Call(uintptr(handle), uintptr(access), uintptr(share), uintptr(flags))
	if windows.Handle(result) == windows.InvalidHandle {
		if callErr == syscall.Errno(0) {
			callErr = windows.ERROR_INVALID_HANDLE
		}
		return windows.InvalidHandle, fmt.Errorf("reopen private object with DACL access: %w", callErr)
	}
	return windows.Handle(result), nil
}

func privateReopenParameters(directory bool) (access, share, flags uint32) {
	access = windows.GENERIC_READ | windows.READ_CONTROL | windows.WRITE_DAC
	share = windows.FILE_SHARE_READ | windows.FILE_SHARE_WRITE | windows.FILE_SHARE_DELETE
	flags = windows.FILE_FLAG_OPEN_REPARSE_POINT
	if directory {
		flags |= windows.FILE_FLAG_BACKUP_SEMANTICS
	}
	return access, share, flags
}

func applyPrivateACL(path string, directory bool) error {
	descriptor, acl, err := privateACL(directory)
	if err != nil {
		return err
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
		return fmt.Errorf("set private ACL: %w", err)
	}
	return nil
}

func applyPrivateACLHandle(handle windows.Handle, directory bool) error {
	descriptor, acl, err := privateACL(directory)
	if err != nil {
		return err
	}
	err = windows.SetSecurityInfo(
		handle,
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil,
		nil,
		acl,
		nil,
	)
	runtime.KeepAlive(descriptor)
	if err != nil {
		return fmt.Errorf("set private ACL on opened handle: %w", err)
	}
	return nil
}

func privateACL(directory bool) (*windows.SECURITY_DESCRIPTOR, *windows.ACL, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, nil, fmt.Errorf("get current user SID: %w", err)
	}

	flags := ""
	if directory {
		flags = "OICI"
	}
	sddl := fmt.Sprintf(
		"D:P(A;%s;GA;;;%s)(A;%s;GA;;;SY)(A;%s;GA;;;BA)",
		flags, user.User.Sid.String(), flags, flags,
	)
	descriptor, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		return nil, nil, fmt.Errorf("build private security descriptor: %w", err)
	}
	acl, _, err := descriptor.DACL()
	if err != nil {
		return nil, nil, fmt.Errorf("extract private DACL: %w", err)
	}
	return descriptor, acl, nil
}

const windowsFileAllAccess windows.ACCESS_MASK = windows.STANDARD_RIGHTS_REQUIRED | windows.SYNCHRONIZE | 0x1ff

func verifyPrivateHandle(handle windows.Handle, directory bool) error {
	descriptor, err := windows.GetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return fmt.Errorf("read DACL from opened handle: %w", err)
	}
	defer runtime.KeepAlive(descriptor)
	control, _, err := descriptor.Control()
	if err != nil {
		return fmt.Errorf("read DACL control: %w", err)
	}
	if control&windows.SE_DACL_PROTECTED == 0 {
		return errors.New("unsafe_private_file: DACL permits inherited access")
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		return fmt.Errorf("read DACL entries: %w", err)
	}
	if dacl == nil {
		return errors.New("unsafe_private_file: DACL is absent or unrestricted")
	}

	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return fmt.Errorf("get current user SID: %w", err)
	}
	system, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		return fmt.Errorf("get SYSTEM SID: %w", err)
	}
	administrators, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return fmt.Errorf("get Administrators SID: %w", err)
	}
	allowed := []*windows.SID{user.User.Sid, system, administrators}
	wantCount := uint16(len(allowed))
	if directory {
		wantCount *= 2
	}
	if dacl.AceCount != wantCount {
		return fmt.Errorf("unsafe_private_file: DACL contains %d entries, want %d", dacl.AceCount, wantCount)
	}
	effectiveSeen := make([]bool, len(allowed))
	propagationSeen := make([]bool, len(allowed))
	propagationFlags := uint8(windows.OBJECT_INHERIT_ACE | windows.CONTAINER_INHERIT_ACE | windows.INHERIT_ONLY_ACE)

	for i := uint16(0); i < dacl.AceCount; i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, uint32(i), &ace); err != nil {
			return fmt.Errorf("read DACL entry %d: %w", i, err)
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			return fmt.Errorf("unsafe_private_file: DACL contains unsupported ACE type %d", ace.Header.AceType)
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		matched := -1
		for index, allowedSID := range allowed {
			if sid.Equals(allowedSID) {
				matched = index
				break
			}
		}
		if matched < 0 {
			return fmt.Errorf("unsafe_private_file: DACL grants access to unexpected SID %s", sid)
		}

		switch {
		case ace.Header.AceFlags == 0 && ace.Mask == windowsFileAllAccess:
			if effectiveSeen[matched] {
				return fmt.Errorf("unsafe_private_file: DACL contains duplicate effective access for SID %s", sid)
			}
			effectiveSeen[matched] = true
		case directory && ace.Header.AceFlags == propagationFlags && ace.Mask == windows.GENERIC_ALL:
			if propagationSeen[matched] {
				return fmt.Errorf("unsafe_private_file: DACL contains duplicate propagation access for SID %s", sid)
			}
			propagationSeen[matched] = true
		default:
			return fmt.Errorf("unsafe_private_file: DACL entry for SID %s has flags %#x mask %#x, want exact native full-access shape", sid, ace.Header.AceFlags, ace.Mask)
		}
	}
	for index, allowedSID := range allowed {
		if !effectiveSeen[index] {
			return fmt.Errorf("unsafe_private_file: DACL omits effective full access for SID %s", allowedSID)
		}
		if directory && !propagationSeen[index] {
			return fmt.Errorf("unsafe_private_file: DACL omits propagation full access for SID %s", allowedSID)
		}
	}
	return nil
}
