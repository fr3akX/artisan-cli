//go:build windows

package releasebuilder

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func processExists(pid int) bool {
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	windows.CloseHandle(handle)
	return true
}

type jobBasicAccounting struct {
	TotalUserTime, TotalKernelTime, ThisPeriodTotalUserTime, ThisPeriodTotalKernelTime int64
	TotalPageFaultCount, TotalProcesses, ActiveProcesses, TotalTerminatedProcesses     uint32
}

func resumeProcessThread(pid uint32) error {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(snapshot)
	entry := windows.ThreadEntry32{Size: uint32(unsafe.Sizeof(windows.ThreadEntry32{}))}
	for err = windows.Thread32First(snapshot, &entry); err == nil; err = windows.Thread32Next(snapshot, &entry) {
		if entry.OwnerProcessID != pid {
			continue
		}
		thread, openErr := windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, entry.ThreadID)
		if openErr != nil {
			return openErr
		}
		_, resumeErr := windows.ResumeThread(thread)
		windows.CloseHandle(thread)
		return resumeErr
	}
	return err
}
func activeJobProcesses(job windows.Handle) (uint32, error) {
	var info jobBasicAccounting
	err := windows.QueryInformationJobObject(job, windows.JobObjectBasicAccountingInformation, uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)), nil)
	return info.ActiveProcesses, err
}
func startContainedProcess(command *exec.Cmd) (func() error, error) {
	command.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_SUSPENDED}
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return func() error { return nil }, err
	}
	closeJob := func() { _ = windows.CloseHandle(job) }
	var limits windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits))); err != nil {
		closeJob()
		return func() error { return nil }, err
	}
	var cancelled atomic.Bool
	command.Cancel = func() error {
		cancelled.Store(true)
		if command.Process == nil {
			return os.ErrProcessDone
		}
		err := windows.TerminateJobObject(job, 1)
		if errors.Is(err, windows.ERROR_ACCESS_DENIED) {
			return os.ErrProcessDone
		}
		return err
	}
	if err := command.Start(); err != nil {
		closeJob()
		return func() error { return nil }, err
	}
	process, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(command.Process.Pid))
	if err != nil {
		_ = command.Process.Kill()
		_ = command.Wait()
		closeJob()
		return func() error { return nil }, err
	}
	defer windows.CloseHandle(process)
	if err := windows.AssignProcessToJobObject(job, process); err != nil {
		_ = command.Process.Kill()
		_ = command.Wait()
		closeJob()
		return func() error { return nil }, err
	}
	if cancelled.Load() {
		_ = windows.TerminateJobObject(job, 1)
		_ = command.Wait()
		closeJob()
		return func() error { return nil }, os.ErrProcessDone
	}
	if err := resumeProcessThread(uint32(command.Process.Pid)); err != nil {
		_ = windows.TerminateJobObject(job, 1)
		_ = command.Wait()
		closeJob()
		return func() error { return nil }, err
	}
	return func() error {
		terminateErr := windows.TerminateJobObject(job, 1)
		deadline := time.Now().Add(2 * time.Second)
		for {
			active, err := activeJobProcesses(job)
			if err != nil {
				closeJob()
				return err
			}
			if active == 0 {
				closeJob()
				if errors.Is(terminateErr, windows.ERROR_ACCESS_DENIED) {
					return nil
				}
				return terminateErr
			}
			if time.Now().After(deadline) {
				closeJob()
				return fmt.Errorf("job retained %d processes after termination", active)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}, nil
}
