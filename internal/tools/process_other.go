//go:build !darwin && !linux && (!windows || (!amd64 && !arm64))

package tools

import (
	"fmt"
	"os/exec"
	"runtime"
)

// configureProcess 在缺少可靠进程树控制的系统上拒绝启动，避免降级为只终止父进程。
// 其他平台仍可使用只读工具、apply_patch 和快照。
func configureProcess(_ *exec.Cmd) (func() error, func(), error) {
	return nil, nil, fmt.Errorf("host command execution is not supported on %s; supported backends are darwin, linux and 64-bit windows", runtime.GOOS)
}
