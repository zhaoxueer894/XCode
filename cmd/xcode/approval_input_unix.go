//go:build darwin || linux

package main

import (
	"context"
	"fmt"
	"strings"
	"syscall"
	"time"
)

// readApprovalLine 以非阻塞方式轮询控制台，在取消时关闭描述符，不留下等待输入的协程。
func readApprovalLine(ctx context.Context) (string, error) {
	fd, err := syscall.Open("/dev/tty", syscall.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return "", err
	}
	defer syscall.Close(fd)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	var line strings.Builder
	buffer := make([]byte, 128)
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		n, err := syscall.Read(fd, buffer)
		if n > 0 {
			line.Write(buffer[:n])
			if line.Len() > 1024 {
				return "", fmt.Errorf("approval input exceeds 1024 bytes")
			}
			if before, _, found := strings.Cut(line.String(), "\n"); found {
				return before, nil
			}
		}
		if err != nil && err != syscall.EAGAIN && err != syscall.EINTR {
			return "", err
		}
		if n == 0 && err == nil {
			return "", fmt.Errorf("approval input closed")
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-ticker.C:
		}
	}
}
