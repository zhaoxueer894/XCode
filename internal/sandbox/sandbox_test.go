package sandbox

// 本文件验证 Docker 后端的参数组装与输出上限；容器实际行为依赖守护进程，由手动验收覆盖。

import (
	"bytes"
	"testing"
)

// TestDockerArgsContainHardConstraints 验证容器参数包含断网、限额、挂载与工作目录约束。
func TestDockerArgsContainHardConstraints(t *testing.T) {
	args := dockerArgs("xcode-test", `D:\ws`, "golang:1.26", []string{"go", "test", "./..."})

	joined := joinArgs(args)
	for _, want := range []string{
		"--network", "none",
		"--pids-limit", "128",
		"--memory", "512m",
		"--name", "xcode-test",
	} {
		if !contains(joined, want) {
			t.Fatalf("docker args missing %q: %v", want, args)
		}
	}
	// 工作区挂载为 /workspace 并作为工作目录。
	if !contains(joined, `D:\ws:/workspace`) || !contains(joined, "-w") || !contains(joined, "/workspace") {
		t.Fatalf("mount or workdir missing: %v", args)
	}
	// 镜像名后紧跟命令本身，命令数组不被 Shell 拼接。
	if !contains(joined, "golang:1.26|go|test|./...") {
		t.Fatalf("image and command tail = %v", args)
	}
}

// joinArgs 用竖线连接参数，便于断言相对顺序。
func joinArgs(args []string) string {
	out := bytes.NewBuffer(nil)
	for i, arg := range args {
		if i > 0 {
			out.WriteByte('|')
		}
		out.WriteString(arg)
	}
	return out.String()
}

// contains 判断子串是否存在。
func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}

// TestLimitedBufferTruncates 验证输出上限：超限后继续计数但不再保留内容。
func TestLimitedBufferTruncates(t *testing.T) {
	var buffer limitedBuffer
	buffer.limit = 8
	buffer.Write([]byte("12345678"))
	if buffer.truncated {
		t.Fatal("exact limit should not truncate")
	}
	buffer.Write([]byte("9ABC"))
	if !buffer.truncated {
		t.Fatal("overflow must truncate")
	}
	if buffer.buffer.String() != "12345678" {
		t.Fatalf("retained = %q", buffer.buffer.String())
	}
	if buffer.written != 12 {
		t.Fatalf("written = %d, want 12", buffer.written)
	}
}

// TestDockerBackendRequiresImage 验证空镜像在启动前被拒绝。
func TestDockerBackendRequiresImage(t *testing.T) {
	outcome := DockerBackend{}.Run(nil, Spec{Command: []string{"ls"}, Timeout: 0})
	if outcome.StartErr == nil {
		t.Fatal("empty image must fail to start")
	}
}

// TestToWSLPath 验证 Windows 盘符路径到 WSL 挂载路径的翻译规则。
func TestToWSLPath(t *testing.T) {
	cases := map[string]string{
		`D:\Projects\XCode`: "/mnt/d/Projects/XCode",
		`D:/Projects/XCode`: "/mnt/d/Projects/XCode",
		`/already/linux`:    "/already/linux",
		`C:\ws`:             "/mnt/c/ws",
	}
	for input, want := range cases {
		if got := toWSLPath(input); got != want {
			t.Fatalf("toWSLPath(%q) = %q, want %q", input, got, want)
		}
	}
}

// TestDockerBackendBridgePrefix 验证桥接前缀：直接模式用 docker，WSL 模式经 wsl.exe 转发。
func TestDockerBackendBridgePrefix(t *testing.T) {
	direct := DockerBackend{}.cliPrefix()
	if len(direct) != 1 || direct[0] != "docker" {
		t.Fatalf("direct prefix = %v", direct)
	}
	bridged := DockerBackend{ViaWSL: true}.cliPrefix()
	if len(bridged) != 3 || bridged[0] != "wsl" || bridged[1] != "-e" || bridged[2] != "docker" {
		t.Fatalf("wsl prefix = %v", bridged)
	}
	// 挂载路径翻译只在 WSL 桥接模式生效。
	if got := (DockerBackend{ViaWSL: true}).mountPath(`D:\ws`); got != "/mnt/d/ws" {
		t.Fatalf("wsl mountPath = %q", got)
	}
	if got := (DockerBackend{}).mountPath(`D:\ws`); got != `D:\ws` {
		t.Fatalf("direct mountPath = %q", got)
	}
}
