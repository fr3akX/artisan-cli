//go:build linux

package releasebuilder

import "fmt"

func directoryHandlePath(fd int) (string, error) {
	return fmt.Sprintf("/proc/self/fd/%d", fd), nil
}
