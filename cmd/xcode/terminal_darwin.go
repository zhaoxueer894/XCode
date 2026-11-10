//go:build darwin

package main

// stdinIsTerminal 用 ioctl 判断标准输入是否为 TTY。
// macOS/BSD 使用 TIOCGETA，与 Linux 的 TCGETS 机制相同、常量不同。

import (
	"os"
	"syscall"
	"unsafe"
)

func stdinIsTerminal() bool {
	var termios syscall.Termios
	_, _, errno := syscall.Syscall6(syscall.SYS_IOCTL, os.Stdin.Fd(), uintptr(syscall.TIOCGETA),
		uintptr(unsafe.Pointer(&termios)), 0, 0, 0)
	return errno == 0
}
