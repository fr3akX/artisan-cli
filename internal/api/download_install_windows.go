//go:build windows

package api

import (
	"errors"
	"os"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/windows"
)

type downloadFileRenameInformationEx struct {
	Flags          uint32
	RootDirectory  windows.Handle
	FileNameLength uint32
	FileName       [1]uint16
}

type downloadLegacyFileRenameInformation struct {
	ReplaceIfExists uint32
	RootDirectory   windows.Handle
	FileNameLength  uint32
	FileName        [1]uint16
}

const (
	downloadRenameReplaceIfExists = uint32(0x1)
	downloadRenamePosixSemantics  = uint32(0x2)
)

func (p *heldWindowsDownloadPublication) publish(target *downloadTarget, force bool) (downloadInstallResult, error) {
	if err := target.verifyHeldSource(); err != nil {
		return downloadInstallResult{}, err
	}
	target.state = downloadTargetNativeAttempted
	target.nativeOperationInvoked = false
	target.nativeOperationErr = nil
	nativeErr := target.operations.nativeOperation(func() error {
		target.nativeOperationInvoked = true
		target.nativeOperationErr = p.renameExactSource(filepath.Base(target.destination), force)
		return target.nativeOperationErr
	})
	var hookErr error
	if target.operations.afterNativeBeforeReconcile != nil {
		hookErr = target.operations.afterNativeBeforeReconcile(target)
	}
	exact, destinationExists, probeErr := p.destinationExact(target)
	if !exact {
		if nativeErr != nil && !target.nativeOperationInvoked && hookErr == nil {
			return downloadInstallResult{Publication: publicationNone, Visibility: visibilityNotVisible, Durability: durabilityNotApplicable}, nativeErr
		}
		if nativeErr != nil && hookErr == nil && p.sourceNameMatches() {
			if !force && destinationExists {
				return downloadInstallResult{Publication: publicationNone, Visibility: visibilityNotVisible, Durability: durabilityNotApplicable}, os.ErrExist
			}
			return downloadInstallResult{Publication: publicationNone, Visibility: visibilityNotVisible, Durability: durabilityNotApplicable}, nativeErr
		}
		return downloadInstallResult{Publication: publicationAmbiguous, Visibility: visibilityAmbiguous, Durability: durabilityUncertain}, errors.Join(errDownloadIdentityAmbiguous, nativeErr, hookErr, probeErr)
	}
	visibility := visibilityExact
	if !p.pathMatches() {
		visibility = visibilityAmbiguous
	}
	result := downloadInstallResult{Publication: publicationExact, Visibility: visibility, Durability: durabilityExact}
	var fileFlushErr error
	if target.operations.flushFile != nil {
		fileFlushErr = target.operations.flushFile(p.source)
	} else {
		fileFlushErr = windows.FlushFileBuffers(windows.Handle(p.source.Fd()))
	}
	// A successful exact-handle rename must flush both file contents and the
	// retained directory namespace before Windows durability can be exact.
	// Attempt the directory flush even when the file flush failed so callers
	// receive every boundary error and no durability step is silently skipped.
	directoryFlushErr := p.flushDirectory(target)
	if fileFlushErr != nil || directoryFlushErr != nil {
		result.Durability = durabilityUncertain
	}
	return result, errors.Join(nativeErr, hookErr, probeErr, fileFlushErr, directoryFlushErr)
}

func (p *heldWindowsDownloadPublication) renameExactSource(leaf string, force bool) error {
	name, err := windows.UTF16FromString(leaf)
	if err != nil {
		return err
	}
	name = name[:len(name)-1]
	var dummy downloadFileRenameInformationEx
	size := int(unsafe.Offsetof(dummy.FileName)) + len(name)*2
	buffer := make([]byte, size)
	info := (*downloadFileRenameInformationEx)(unsafe.Pointer(&buffer[0]))
	info.Flags = downloadRenamePosixSemantics
	if force {
		info.Flags |= downloadRenameReplaceIfExists
	}
	info.RootDirectory = p.parent
	info.FileNameLength = uint32(len(name) * 2)
	copy((*[windows.MAX_LONG_PATH]uint16)(unsafe.Pointer(&info.FileName[0]))[:len(name):len(name)], name)
	err = windows.SetFileInformationByHandle(windows.Handle(p.source.Fd()), windows.FileRenameInfoEx, &buffer[0], uint32(len(buffer)))
	if err == nil || !unsupportedWindowsRenameEx(err) {
		return err
	}
	return p.renameExactSourceLegacy(name, force)
}

func unsupportedWindowsRenameEx(err error) bool {
	return errors.Is(err, windows.ERROR_INVALID_PARAMETER) || errors.Is(err, windows.ERROR_NOT_SUPPORTED) || errors.Is(err, windows.ERROR_CALL_NOT_IMPLEMENTED)
}

func (p *heldWindowsDownloadPublication) renameExactSourceLegacy(name []uint16, force bool) error {
	var dummy downloadLegacyFileRenameInformation
	size := int(unsafe.Offsetof(dummy.FileName)) + len(name)*2
	buffer := make([]byte, size)
	info := (*downloadLegacyFileRenameInformation)(unsafe.Pointer(&buffer[0]))
	if force {
		info.ReplaceIfExists = 1
	}
	info.RootDirectory = p.parent
	info.FileNameLength = uint32(len(name) * 2)
	copy((*[windows.MAX_LONG_PATH]uint16)(unsafe.Pointer(&info.FileName[0]))[:len(name):len(name)], name)
	var status windows.IO_STATUS_BLOCK
	return windows.NtSetInformationFile(windows.Handle(p.source.Fd()), &status, &buffer[0], uint32(len(buffer)), windows.FileRenameInformation)
}

func (p *heldWindowsDownloadPublication) destinationExact(target *downloadTarget) (bool, bool, error) {
	handle, err := openWindowsDownloadRelative(p.parent, filepath.Base(target.destination), windows.GENERIC_READ, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE)
	if err != nil {
		if errors.Is(err, windows.ERROR_FILE_NOT_FOUND) || errors.Is(err, windows.ERROR_PATH_NOT_FOUND) {
			return false, false, err
		}
		return false, false, err
	}
	file := os.NewFile(uintptr(handle), target.destination)
	defer file.Close()
	info, err := windowsDownloadInfo(handle)
	if err != nil {
		return false, true, err
	}
	count, digest, hashErr := digestDownloadDescriptor(file)
	return sameWindowsDownloadInfo(info, p.sourceInfo) && hashErr == nil && count == target.sealedCount && digest == target.sealedDigest, true, hashErr
}
