//go:build !darwin && !linux && !windows

package main

// stdinIsTerminal 对无终端检测实现的平台保守返回 false，向导走降级提示。

func stdinIsTerminal() bool { return false }
