//go:build windows

package releasebuilder

import (
	"errors"
	"fmt"
	"io"
	"os"

	"golang.org/x/sys/windows"
)

func readRegularSnapshot(path string, maximum int64) ([]byte, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(name, windows.GENERIC_READ, windows.FILE_SHARE_READ, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(handle), path)
	if file == nil {
		windows.CloseHandle(handle)
		return nil, errors.New("open snapshot handle")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if isLinkOrReparse(info) || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("not a regular non-reparse file: %s", path)
	}
	contents, err := io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil {
		return nil, err
	}
	if int64(len(contents)) > maximum {
		return nil, fmt.Errorf("file exceeds %d bytes: %s", maximum, path)
	}
	return contents, nil
}
