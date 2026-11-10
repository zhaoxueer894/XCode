//go:build windows

package main

// stdinIsTerminal 判断标准输入是否连接到真实控制台。
// GetConsoleMode 只对控制台输入句柄成功；NUL 设备、管道、重定向文件都会失败，
// 因此比 Stat 的 ModeCharDevice 更可靠（NUL 在 Windows 上也是字符设备）。

import (
	"os"
	"syscall"
	"unsafe"
)

func stdinIsTerminal() bool {
	kernel := syscall.NewLazyDLL("kernel32.dll")
	getConsoleMode := kernel.NewProc("GetConsoleMode")
	var consoleMode uint32
	handle := syscall.Handle(os.Stdin.Fd())
	ret, _, _ := getConsoleMode.Call(uintptr(handle), uintptr(unsafe.Pointer(&consoleMode)))
	return ret != 0
}
