//go:build darwin

package releasebuilder

import (
	"bytes"
	"errors"
	"unsafe"

	"golang.org/x/sys/unix"
)

func directoryHandlePath(fd int) (string, error) {
	buffer := make([]byte, unix.PathMax)
	_, _, errno := unix.Syscall(
		unix.SYS_FCNTL,
		uintptr(fd),
		uintptr(unix.F_GETPATH),
		uintptr(unsafe.Pointer(&buffer[0])),
	)
	if errno != 0 {
		return "", errno
	}
	nul := bytes.IndexByte(buffer, 0)
	if nul < 0 {
		return "", errors.New("F_GETPATH result is missing NUL terminator")
	}
	return string(buffer[:nul]), nil
}
