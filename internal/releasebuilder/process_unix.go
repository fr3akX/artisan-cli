//go:build !windows

package releasebuilder

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"
)

func killProcessGroup(pid int) error {
	err := syscall.Kill(-pid, syscall.SIGKILL)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}
func processExists(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
func processGroupGone(pid int) bool {
	err := syscall.Kill(-pid, 0)
	return errors.Is(err, syscall.ESRCH)
}

// startContainedProcess cleans up the direct process group. A trusted helper
// must not deliberately setsid/setpgid away; this is leak containment, not a
// complete descendant sandbox.
func startContainedProcess(command *exec.Cmd) (func() error, error) {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Cancel = func() error {
		if command.Process == nil {
			return os.ErrProcessDone
		}
		if err := killProcessGroup(command.Process.Pid); err != nil {
			return err
		}
		return nil
	}
	if err := command.Start(); err != nil {
		return func() error { return nil }, err
	}
	pid := command.Process.Pid
	return func() error {
		if err := killProcessGroup(pid); err != nil {
			return err
		}
		deadline := time.Now().Add(2 * time.Second)
		for !processGroupGone(pid) {
			if time.Now().After(deadline) {
				return fmt.Errorf("process group %d remained after termination", pid)
			}
			time.Sleep(10 * time.Millisecond)
		}
		return nil
	}, nil
}
