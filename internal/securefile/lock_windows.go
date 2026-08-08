//go:build windows

package securefile

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func acquirePrivateLock(ctx context.Context, dir, name string, maxWait time.Duration) (func() error, error) {
	directory, err := openPrivateLockDirectory(dir)
	if err != nil {
		return nil, err
	}
	defer directory.Close()

	objectName, err := windows.NewNTUnicodeString(name)
	if err != nil {
		return nil, fmt.Errorf("encode private lock name: %w", err)
	}
	attributes := &windows.OBJECT_ATTRIBUTES{
		Length:        uint32(unsafe.Sizeof(windows.OBJECT_ATTRIBUTES{})),
		RootDirectory: windows.Handle(directory.Fd()),
		ObjectName:    objectName,
		Attributes:    windows.OBJ_CASE_INSENSITIVE | windows.OBJ_DONT_REPARSE,
	}

	deadline := time.NewTimer(maxWait)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var handle windows.Handle
		var status windows.IO_STATUS_BLOCK
		allocationSize := int64(0)
		openErr := windows.NtCreateFile(
			&handle,
			windows.FILE_GENERIC_READ|windows.FILE_GENERIC_WRITE|windows.READ_CONTROL|windows.WRITE_DAC,
			attributes,
			&status,
			&allocationSize,
			windows.FILE_ATTRIBUTE_NORMAL,
			0,
			windows.FILE_OPEN_IF,
			windows.FILE_NON_DIRECTORY_FILE|windows.FILE_OPEN_REPARSE_POINT|windows.FILE_SYNCHRONOUS_IO_NONALERT,
			0,
			0,
		)
		if openErr == nil {
			if contextErr := ctx.Err(); contextErr != nil {
				windows.CloseHandle(handle)
				return nil, contextErr
			}
			var information windows.ByHandleFileInformation
			if err := windows.GetFileInformationByHandle(handle, &information); err != nil {
				windows.CloseHandle(handle)
				return nil, fmt.Errorf("inspect private lock: %w", err)
			}
			if information.FileAttributes&(windows.FILE_ATTRIBUTE_REPARSE_POINT|windows.FILE_ATTRIBUTE_DIRECTORY) != 0 {
				windows.CloseHandle(handle)
				return nil, errors.New("unsafe_private_file: lock must be a regular non-reparse file")
			}
			file := os.NewFile(uintptr(handle), name)
			if err := applyPrivateACLHandle(handle, false); err != nil {
				file.Close()
				return nil, err
			}
			if err := verifyPrivateHandle(handle, false); err != nil {
				file.Close()
				return nil, err
			}
			if err := file.Truncate(0); err != nil {
				closeErr := file.Close()
				return nil, errors.Join(fmt.Errorf("empty private lock: %w", err), closeErr)
			}
			if err := file.Sync(); err != nil {
				closeErr := file.Close()
				return nil, errors.Join(fmt.Errorf("sync empty private lock: %w", err), closeErr)
			}
			return file.Close, nil
		}
		if !errors.Is(openErr, windows.STATUS_SHARING_VIOLATION) &&
			!errors.Is(openErr, windows.STATUS_FILE_LOCK_CONFLICT) &&
			!errors.Is(openErr, windows.STATUS_LOCK_NOT_GRANTED) {
			return nil, fmt.Errorf("open private lock: %w", openErr)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-deadline.C:
			return nil, errors.New("private lock acquisition timed out")
		case <-ticker.C:
		}
	}
}

func openPrivateLockDirectory(path string) (*os.File, error) {
	pathPointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, fmt.Errorf("encode private lock directory: %w", err)
	}
	handle, err := windows.CreateFile(
		pathPointer,
		windows.FILE_GENERIC_READ|windows.FILE_GENERIC_WRITE|windows.READ_CONTROL,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
	if err != nil {
		return nil, &os.PathError{Op: "open private lock directory", Path: path, Err: err}
	}
	var information windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &information); err != nil {
		windows.CloseHandle(handle)
		return nil, fmt.Errorf("inspect private lock directory: %w", err)
	}
	if information.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 ||
		information.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 {
		windows.CloseHandle(handle)
		return nil, errors.New("unsafe_private_file: lock directory must be a non-reparse directory")
	}
	if err := verifyPrivateHandle(handle, true); err != nil {
		windows.CloseHandle(handle)
		return nil, err
	}
	return os.NewFile(uintptr(handle), path), nil
}
