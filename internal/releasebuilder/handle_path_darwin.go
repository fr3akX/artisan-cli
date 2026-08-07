//go:build darwin

package releasebuilder

import "fmt"

func directoryHandlePath(fd int) string { return fmt.Sprintf("/dev/fd/%d", fd) }
