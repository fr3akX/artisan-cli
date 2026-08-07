//go:build unix

package integration

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

type processTree struct {
	mu                   sync.Mutex
	processGroupID       int
	terminationRequested bool
}

func prepareProcessTree(command *exec.Cmd) (*processTree, error) {
	if command.SysProcAttr != nil {
		return nil, errors.New("process tree attributes were already configured")
	}
	tree := &processTree{}
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Cancel = tree.terminate
	return tree, nil
}

func (tree *processTree) afterStart(process *os.Process) error {
	tree.mu.Lock()
	tree.processGroupID = process.Pid
	terminate := tree.terminationRequested
	tree.mu.Unlock()
	if terminate {
		err := tree.killGroup(process.Pid)
		if errors.Is(err, os.ErrProcessDone) {
			return nil
		}
		return err
	}
	return nil
}

func (tree *processTree) terminate() error {
	tree.mu.Lock()
	tree.terminationRequested = true
	processGroupID := tree.processGroupID
	tree.mu.Unlock()
	if processGroupID == 0 {
		return nil
	}
	return tree.killGroup(processGroupID)
}

func (tree *processTree) killGroup(processGroupID int) error {
	err := syscall.Kill(-processGroupID, syscall.SIGKILL)
	if errors.Is(err, syscall.ESRCH) {
		return os.ErrProcessDone
	}
	if err != nil {
		return errors.New("could not terminate isolated process group")
	}
	return nil
}

func (tree *processTree) close(timeout time.Duration) error {
	if err := tree.terminate(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}
	tree.mu.Lock()
	processGroupID := tree.processGroupID
	tree.mu.Unlock()
	if processGroupID == 0 {
		return nil
	}
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		err := syscall.Kill(-processGroupID, 0)
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		if err != nil {
			return errors.New("could not verify isolated process group termination")
		}
		select {
		case <-deadline.C:
			return errors.New("isolated process group termination exceeded its bound")
		case <-ticker.C:
		}
	}
}

func TestCLIRunnerTimeoutTerminatesDescendantProcess(t *testing.T) {
	root := t.TempDir()
	runDirectory := filepath.Join(root, "run")
	if err := os.Mkdir(runDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	pidPath := filepath.Join(root, "child.pid")
	script := filepath.Join(root, "process-tree-holder")
	contents := "#!/bin/sh\nsleep 30 &\nchild=$!\nprintf '%s\\n' \"$child\" > \"$PID_FILE\"\nwait\n"
	if err := os.WriteFile(script, []byte(contents), 0o700); err != nil {
		t.Fatal(err)
	}
	runner := cliRunner{
		binary: script, baseURL: "http://127.0.0.1", cwd: runDirectory,
		env:            []string{"PATH=" + os.Getenv("PATH"), "PID_FILE=" + pidPath},
		commandTimeout: 250 * time.Millisecond, commandWaitDelay: 250 * time.Millisecond,
	}
	execution := runner.execute("")
	if !execution.timedOut {
		t.Fatalf("process-tree command timedOut = false; error type %T", execution.err)
	}
	pidBytes, err := os.ReadFile(pidPath)
	if err != nil {
		t.Fatalf("timed-out command did not record descendant PID: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(pidBytes)))
	if err != nil || pid <= 0 {
		t.Fatalf("invalid descendant PID record")
	}
	defer func() { _ = syscall.Kill(pid, syscall.SIGKILL) }()
	if err := syscall.Kill(pid, 0); err == nil || !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("descendant PID %d still exists after execute returned", pid)
	}
}
