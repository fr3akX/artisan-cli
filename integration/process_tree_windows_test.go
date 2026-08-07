//go:build windows

package integration

import (
	"errors"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var ntResumeProcess = windows.NewLazySystemDLL("ntdll.dll").NewProc("NtResumeProcess")

type processTree struct {
	mu                   sync.Mutex
	job                  windows.Handle
	processAssigned      bool
	terminationRequested bool
}

type jobBasicAccountingInformation struct {
	TotalUserTime             int64
	TotalKernelTime           int64
	ThisPeriodTotalUserTime   int64
	ThisPeriodTotalKernelTime int64
	TotalPageFaultCount       uint32
	TotalProcesses            uint32
	ActiveProcesses           uint32
	TerminatedProcesses       uint32
}

func prepareProcessTree(command *exec.Cmd) (*processTree, error) {
	if command.SysProcAttr != nil {
		return nil, errors.New("process tree attributes were already configured")
	}
	if err := ntResumeProcess.Find(); err != nil {
		return nil, errors.New("native process resume support was unavailable")
	}
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, errors.New("could not create process containment job")
	}
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(
		job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&limits)),
		uint32(unsafe.Sizeof(limits)),
	); err != nil {
		_ = windows.CloseHandle(job)
		return nil, errors.New("could not configure process containment job")
	}
	tree := &processTree{job: job}
	command.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_SUSPENDED}
	command.Cancel = tree.terminate
	return tree, nil
}

func (tree *processTree) afterStart(process *os.Process) error {
	rights := uint32(windows.PROCESS_SET_QUOTA | windows.PROCESS_TERMINATE | windows.PROCESS_SUSPEND_RESUME | windows.PROCESS_QUERY_INFORMATION)
	processHandle, err := windows.OpenProcess(rights, false, uint32(process.Pid))
	if err != nil {
		_ = process.Kill()
		return errors.New("could not open suspended process for containment")
	}
	defer windows.CloseHandle(processHandle)

	tree.mu.Lock()
	defer tree.mu.Unlock()
	if tree.job == 0 {
		_ = windows.TerminateProcess(processHandle, 1)
		return errors.New("process containment job was already closed")
	}
	if err := windows.AssignProcessToJobObject(tree.job, processHandle); err != nil {
		_ = windows.TerminateProcess(processHandle, 1)
		return errors.New("could not assign suspended process to containment job")
	}
	tree.processAssigned = true
	if tree.terminationRequested {
		if err := windows.TerminateJobObject(tree.job, 1); err != nil {
			return errors.New("could not terminate process containment job")
		}
		return nil
	}
	status, _, _ := ntResumeProcess.Call(uintptr(processHandle))
	if windows.NTStatus(status) != 0 {
		_ = windows.TerminateJobObject(tree.job, 1)
		return errors.New("could not resume contained process")
	}
	return nil
}

func (tree *processTree) terminate() error {
	tree.mu.Lock()
	defer tree.mu.Unlock()
	tree.terminationRequested = true
	if tree.job == 0 || !tree.processAssigned {
		return nil
	}
	active, err := activeJobProcesses(tree.job)
	if err != nil {
		return errors.New("could not inspect process containment job")
	}
	if active == 0 {
		return os.ErrProcessDone
	}
	if err := windows.TerminateJobObject(tree.job, 1); err != nil {
		return errors.New("could not terminate process containment job")
	}
	return nil
}

func (tree *processTree) close(timeout time.Duration) error {
	tree.mu.Lock()
	job := tree.job
	if job == 0 {
		tree.mu.Unlock()
		return nil
	}
	tree.terminationRequested = true
	active, queryErr := activeJobProcesses(job)
	if queryErr == nil && active != 0 {
		queryErr = windows.TerminateJobObject(job, 1)
	}
	tree.mu.Unlock()

	var waitErr error
	if queryErr == nil {
		deadline := time.NewTimer(timeout)
		defer deadline.Stop()
		ticker := time.NewTicker(5 * time.Millisecond)
		defer ticker.Stop()
		for {
			currentActive, err := activeJobProcesses(job)
			if err != nil {
				waitErr = errors.New("could not verify process containment job termination")
				break
			}
			if currentActive == 0 {
				break
			}
			select {
			case <-deadline.C:
				waitErr = errors.New("process containment job termination exceeded its bound")
			case <-ticker.C:
				continue
			}
			break
		}
	} else {
		waitErr = errors.New("could not terminate process containment job")
	}

	tree.mu.Lock()
	if tree.job == job {
		tree.job = 0
	}
	tree.mu.Unlock()
	if err := windows.CloseHandle(job); err != nil && waitErr == nil {
		waitErr = errors.New("could not close process containment job")
	}
	return waitErr
}

func activeJobProcesses(job windows.Handle) (uint32, error) {
	var accounting jobBasicAccountingInformation
	if err := windows.QueryInformationJobObject(
		job,
		windows.JobObjectBasicAccountingInformation,
		uintptr(unsafe.Pointer(&accounting)),
		uint32(unsafe.Sizeof(accounting)),
		nil,
	); err != nil {
		return 0, err
	}
	return accounting.ActiveProcesses, nil
}
