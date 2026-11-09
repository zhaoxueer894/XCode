package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestExtractRegStringValue 验证 reg query 输出解析（值可含空格）。
func TestExtractRegStringValue(t *testing.T) {
	output := "\r\nHKEY_CURRENT_USER/Environment\r\n    Path    REG_EXPAND_SZ    C:/Users/me/bin;C:/Tools\r\n"
	value, ok := extractRegStringValue(output)
	if !ok || value != "C:/Users/me/bin;C:/Tools" {
		t.Fatalf("value = %q ok=%v, want full path list", value, ok)
	}
	if _, ok := extractRegStringValue("no type marker here"); ok {
		t.Fatal("unexpected success without REG_ marker")
	}
}

// TestCopyExecutable 验证自复制保留执行权限且内容一致。
func TestCopyExecutable(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "xcode-origin.exe")
	if err := os.WriteFile(source, []byte("binary-bytes"), 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "nested", "xcode.exe")
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := copyExecutable(source, target); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "binary-bytes" {
		t.Fatalf("copied content = %q", data)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o500 == 0 {
		t.Fatalf("copied file lost execute permission: %v", info.Mode())
	}
}

// TestPathListContains 验证 PATH 目录匹配忽略大小写与斜杠差异。
func TestPathListContains(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", strings.Join([]string{`C:\Windows`, dir}, string(filepath.ListSeparator)))
	if !pathListContains(filepath.Join(dir, "sub", "..")) {
		t.Fatal("cleaned path should match PATH entry")
	}
	if pathListContains(filepath.Join(dir, "missing")) {
		t.Fatal("unrelated directory matched")
	}
}
