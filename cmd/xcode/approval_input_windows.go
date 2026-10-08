//go:build windows

package main

import (
	"context"
	"fmt"
	"os"
	"syscall"
	"time"
	"unsafe"
)

// consoleKey 对应 INPUT_RECORD 中的 KEY_EVENT_RECORD，字段偏移由 Windows ABI 定义。
type consoleKey struct {
	Kind      uint16 // 保存事件种类；1 表示键盘事件。
	Padding   uint16 // 保存联合体之前的对齐空位。
	Down      int32  // 表示按下按键，松开事件不进入输入内容。
	Repeat    uint16 // 保存按键重复次数。
	Virtual   uint16 // 保存虚拟键码。
	Scan      uint16 // 保存硬件扫描码。
	Character uint16 // 保存输入字符；审批只接收可打印 ASCII。
	Control   uint32 // 保存 Shift/Ctrl 等控制键状态。
}

// readApprovalLine 轮询控制台事件，自行处理回车和退格，使取消无需等待一行输入结束。
func readApprovalLine(ctx context.Context) (string, error) {
	input, err := os.Open("CONIN$")
	if err != nil {
		return "", err
	}
	defer input.Close()
	kernel := syscall.NewLazyDLL("kernel32.dll")
	countEvents := kernel.NewProc("GetNumberOfConsoleInputEvents")
	readEvent := kernel.NewProc("ReadConsoleInputW")
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	line := make([]byte, 0, 16)
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		var count uint32
		if ok, _, err := countEvents.Call(input.Fd(), uintptr(unsafe.Pointer(&count))); ok == 0 {
			return "", err
		}
		if count > 0 {
			var event consoleKey
			var read uint32
			if ok, _, err := readEvent.Call(input.Fd(), uintptr(unsafe.Pointer(&event)), 1, uintptr(unsafe.Pointer(&read))); ok == 0 {
				return "", err
			}
			if read == 0 || event.Kind != 1 || event.Down == 0 {
				continue
			}
			switch event.Character {
			case '\r':
				fmt.Fprintln(os.Stderr)
				return string(line), nil
			case '\b':
				if len(line) > 0 {
					line = line[:len(line)-1]
					fmt.Fprint(os.Stderr, "\b \b")
				}
			default:
				if event.Character >= 32 && event.Character <= 126 && len(line) < 16 {
					line = append(line, byte(event.Character))
					fmt.Fprintf(os.Stderr, "%c", event.Character)
				}
			}
			continue
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-ticker.C:
		}
	}
}
