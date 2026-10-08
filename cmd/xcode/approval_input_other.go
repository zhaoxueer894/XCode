//go:build !darwin && !linux && !windows

package main

import (
	"context"
	"fmt"
)

// readApprovalLine 对没有控制台实现的平台返回明确错误，审批不会默认放行。
func readApprovalLine(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return "", fmt.Errorf("interactive approval is unsupported on this platform")
}
