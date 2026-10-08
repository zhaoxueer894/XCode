//go:build darwin || linux

package tools

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// configureProcess 把命令放进独立进程组；取消和正常退出后都清理同组后代。
func configureProcess(command *exec.Cmd) (func() error, func(), error) {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	kill := func() error {
		if command.Process == nil {
			return nil
		}
		err := syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	command.Cancel = kill
	return func() error { return nil }, func() { _ = kill() }, nil
}
