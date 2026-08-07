//go:build windows

package releasebuilder

import (
	"os"
	"os/exec"

	"golang.org/x/sys/windows"
)

func startContainedProcess(command *exec.Cmd) (func(), error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return func() {}, err
	}
	cleanup := func() { _ = windows.CloseHandle(job) }
	command.Cancel = func() error {
		if command.Process == nil {
			return os.ErrProcessDone
		}
		return windows.TerminateJobObject(job, 1)
	}
	if err := command.Start(); err != nil {
		cleanup()
		return func() {}, err
	}
	process, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(command.Process.Pid))
	if err != nil {
		_ = command.Process.Kill()
		_ = command.Wait()
		cleanup()
		return func() {}, err
	}
	defer windows.CloseHandle(process)
	if err := windows.AssignProcessToJobObject(job, process); err != nil {
		_ = command.Process.Kill()
		_ = command.Wait()
		cleanup()
		return func() {}, err
	}
	return cleanup, nil
}
