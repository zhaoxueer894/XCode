//go:build darwin || linux

package main

// stdinIsTerminal 用 ioctl 判断标准输入是否为 TTY。
// TCGETS/TIOCGETA 只对终端设备成功；/dev/null 虽是字符设备但不是 TTY。

import (
	"os"
	"syscall"
	"unsafe"
)

func stdinIsTerminal() bool {
	request := uintptr(syscall.TCGETS)
	// macOS/BSD 使用 TIOCGETA；Linux 使用 TCGETS。两者常量值不同但机制相同。
	if syscall.TCGETS == 0 {
		request = uintptr(syscall.TIOCGETA)
	}
	var termios syscall.Termios
	_, _, errno := syscall.Syscall6(syscall.SYS_IOCTL, os.Stdin.Fd(), request,
		uintptr(unsafe.Pointer(&termios)), 0, 0, 0)
	return errno == 0
}
