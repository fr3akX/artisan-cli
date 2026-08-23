//go:build windows

package api

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

// createDownloadTemp creates the file with DELETE access and delete sharing so
// a separately held identity handle can publish this exact object on Windows.
func createDownloadTemp(directory, pattern string) (*os.File, error) {
	if directory == "" {
		directory = "."
	}
	for attempt := 0; attempt < 100; attempt++ {
		var random [16]byte
		if _, err := rand.Read(random[:]); err != nil {
			return nil, err
		}
		name := pattern + hex.EncodeToString(random[:])
		if index := strings.LastIndexByte(pattern, '*'); index >= 0 {
			name = pattern[:index] + hex.EncodeToString(random[:]) + pattern[index+1:]
		}
		path := filepath.Join(directory, name)
		pointer, err := windows.UTF16PtrFromString(path)
		if err != nil {
			return nil, err
		}
		handle, err := windows.CreateFile(
			pointer,
			windows.GENERIC_READ|windows.GENERIC_WRITE|windows.DELETE|windows.READ_CONTROL,
			windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
			nil,
			windows.CREATE_NEW,
			windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT,
			0,
		)
		if err != nil {
			if errors.Is(err, windows.ERROR_FILE_EXISTS) || errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
				continue
			}
			return nil, &os.PathError{Op: "createtemp", Path: path, Err: err}
		}
		return os.NewFile(uintptr(handle), path), nil
	}
	return nil, errors.New("could not allocate download temporary file")
}
