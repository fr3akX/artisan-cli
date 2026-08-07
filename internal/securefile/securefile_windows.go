//go:build windows

package securefile

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

// OpenPrivate opens a file without traversing a final reparse point and
// verifies the DACL on the exact handle used for subsequent reads.
func OpenPrivate(path string) (*os.File, error) {
	return openWindowsObject(path, false, true)
}

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
	if err := applyPrivateACL(file.Name(), directory); err != nil {
		return err
	}
	return verifyPrivateHandle(windows.Handle(file.Fd()), directory)
}

func applyPrivateACL(path string, directory bool) error {
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

	inheritance := uint32(windows.NO_INHERITANCE)
	if directory {
		inheritance = windows.OBJECT_INHERIT_ACE | windows.CONTAINER_INHERIT_ACE
	}
	entries := []windows.EXPLICIT_ACCESS{
		privateAccessEntry(user.User.Sid, windows.TRUSTEE_IS_USER, inheritance),
		privateAccessEntry(system, windows.TRUSTEE_IS_USER, inheritance),
		privateAccessEntry(administrators, windows.TRUSTEE_IS_GROUP, inheritance),
	}
	acl, err := windows.ACLFromEntries(entries, nil)
	runtime.KeepAlive(user)
	runtime.KeepAlive(system)
	runtime.KeepAlive(administrators)
	if err != nil {
		return fmt.Errorf("build private ACL: %w", err)
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
		return fmt.Errorf("set private ACL: %w", err)
	}
	return nil
}

func privateAccessEntry(sid *windows.SID, trusteeType windows.TRUSTEE_TYPE, inheritance uint32) windows.EXPLICIT_ACCESS {
	return windows.EXPLICIT_ACCESS{
		AccessPermissions: windows.GENERIC_ALL,
		AccessMode:        windows.GRANT_ACCESS,
		Inheritance:       inheritance,
		Trustee: windows.TRUSTEE{
			TrusteeForm:  windows.TRUSTEE_IS_SID,
			TrusteeType:  trusteeType,
			TrusteeValue: windows.TrusteeValueFromSID(sid),
		},
	}
}

func verifyPrivateHandle(handle windows.Handle, directory bool) error {
	descriptor, err := windows.GetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return fmt.Errorf("read DACL from opened handle: %w", err)
	}
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
	seen := make([]bool, len(allowed))

	expectedFlags := uint8(0)
	if directory {
		expectedFlags = windows.OBJECT_INHERIT_ACE | windows.CONTAINER_INHERIT_ACE
	}
	for i := uint16(0); i < dacl.AceCount; i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, uint32(i), &ace); err != nil {
			return fmt.Errorf("read DACL entry %d: %w", i, err)
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			return fmt.Errorf("unsafe_private_file: DACL contains unsupported ACE type %d", ace.Header.AceType)
		}
		if ace.Header.AceFlags != expectedFlags {
			return fmt.Errorf("unsafe_private_file: DACL ACE flags %#x do not match required %#x", ace.Header.AceFlags, expectedFlags)
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
		if seen[matched] {
			return fmt.Errorf("unsafe_private_file: DACL contains duplicate access for SID %s", sid)
		}
		if ace.Mask&windows.GENERIC_ALL == 0 {
			return fmt.Errorf("unsafe_private_file: DACL does not grant full access to SID %s", sid)
		}
		seen[matched] = true
	}
	for index, present := range seen {
		if !present {
			return fmt.Errorf("unsafe_private_file: DACL omits required SID %s", allowed[index])
		}
	}
	return nil
}
