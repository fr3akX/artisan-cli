//go:build windows

package auth

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

func preparePrivateDirectory(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	if err := applyPrivateACL(path, true); err != nil {
		return err
	}
	return verifyPrivateACL(path)
}

func applyPrivatePermissions(path string) error {
	return applyPrivateACL(path, false)
}

func verifyPrivatePermissions(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return err
		}
		return fmt.Errorf("inspect credentials: %w", err)
	}
	if !info.Mode().IsRegular() {
		return errors.New("unsafe_credentials: credential path is not a regular file")
	}
	if err := verifyPrivateACL(path); err != nil {
		return fmt.Errorf("unsafe_credentials: %w", err)
	}
	return nil
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
		inheritance = windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT
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

func verifyPrivateACL(path string) error {
	descriptor, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return fmt.Errorf("read DACL: %w", err)
	}
	control, _, err := descriptor.Control()
	if err != nil {
		return fmt.Errorf("read DACL control: %w", err)
	}
	if control&windows.SE_DACL_PROTECTED == 0 {
		return errors.New("DACL permits inherited access")
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		return fmt.Errorf("read DACL entries: %w", err)
	}
	if dacl == nil {
		return errors.New("DACL is absent or unrestricted")
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

	for i := uint16(0); i < dacl.AceCount; i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, uint32(i), &ace); err != nil {
			return fmt.Errorf("read DACL entry %d: %w", i, err)
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			return fmt.Errorf("DACL contains unsupported ACE type %d", ace.Header.AceType)
		}
		if ace.Header.AceFlags&windows.INHERITED_ACE != 0 {
			return errors.New("DACL contains an inherited ACE")
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
			return fmt.Errorf("DACL grants access to unexpected SID %s", sid)
		}
		if seen[matched] {
			return fmt.Errorf("DACL contains duplicate access for SID %s", sid)
		}
		if ace.Mask&windows.GENERIC_ALL == 0 {
			return fmt.Errorf("DACL does not grant full access to SID %s", sid)
		}
		seen[matched] = true
	}
	for index, present := range seen {
		if !present {
			return fmt.Errorf("DACL omits required SID %s", allowed[index])
		}
	}
	return nil
}
