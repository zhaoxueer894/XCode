//go:build linux

package main

// stdinIsTerminal 用 ioctl 判断标准输入是否为 TTY。
// TCGETS 只对终端设备成功；/dev/null 虽是字符设备但不是 TTY。

import (
	"os"
	"syscall"
	"unsafe"
)

func stdinIsTerminal() bool {
	var termios syscall.Termios
	_, _, errno := syscall.Syscall6(syscall.SYS_IOCTL, os.Stdin.Fd(), uintptr(syscall.TCGETS),
		uintptr(unsafe.Pointer(&termios)), 0, 0, 0)
	return errno == 0
}
