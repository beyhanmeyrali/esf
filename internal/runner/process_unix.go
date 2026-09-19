//go:build darwin || linux

package runner

import (
	"errors"
	"os"
	"os/exec"
	"runtime"
	"syscall"
)

func configureProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func terminateProcessTree(process *os.Process) error {
	if process == nil {
		return nil
	}
	err := syscall.Kill(-process.Pid, syscall.SIGKILL)
	if ignorableProcessTreeTerminationError(err, func() error {
		return syscall.Kill(-process.Pid, 0)
	}) {
		return nil
	}
	return err
}

func ignorableProcessTreeTerminationError(err error, probeProcessGroup func() error) bool {
	if errors.Is(err, syscall.ESRCH) {
		return true
	}
	return runtime.GOOS == "darwin" && errors.Is(err, syscall.EPERM) && errors.Is(probeProcessGroup(), syscall.ESRCH)
}

func processExitCode(state *os.ProcessState) int {
	if exitCode := state.ExitCode(); exitCode >= 0 {
		return exitCode
	}
	if status, ok := state.Sys().(syscall.WaitStatus); ok && status.Signaled() {
		return 128 + int(status.Signal())
	}
	return 1
}

func terminatedExitCode() int {
	return 128 + int(syscall.SIGKILL)
}
