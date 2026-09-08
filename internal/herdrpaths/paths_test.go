package herdrpaths

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// TestConfigRootHonorsXDGOverride XDG_CONFIG_HOME 在所有平台都必须优先，
// 测试夹具和自定义部署都依赖它。
func TestConfigRootHonorsXDGOverride(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	root, err := ConfigRoot()
	if err != nil {
		t.Fatal(err)
	}
	if root != dir {
		t.Fatalf("expected XDG override %s, got %s", dir, root)
	}
	herdrDir, err := HerdrDir()
	if err != nil {
		t.Fatal(err)
	}
	if herdrDir != filepath.Join(dir, "herdr") {
		t.Fatalf("unexpected herdr dir: %s", herdrDir)
	}
}

// TestConfigRootIgnoresRelativeXDG 相对路径的 XDG_CONFIG_HOME 按规范应被忽略，
// 否则 socket 路径会依赖进程的工作目录。
func TestConfigRootIgnoresRelativeXDG(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "relative/path")
	root, err := ConfigRoot()
	if err != nil {
		t.Fatal(err)
	}
	if root == "relative/path" {
		t.Fatal("relative XDG_CONFIG_HOME must be ignored")
	}
	if !filepath.IsAbs(root) {
		t.Fatalf("config root must be absolute, got %s", root)
	}
}

// TestDarwinDefaultsToDotConfig macOS 上必须解析到 ~/.config，因为 Herdr 把
// herdr.sock 放在那里；用 os.UserConfigDir() 的 ~/Library/Application Support
// 会让受控端拒绝真实 socket，终端打不开。
func TestDarwinDefaultsToDotConfig(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("darwin-only default")
	}
	t.Setenv("XDG_CONFIG_HOME", "")
	root, err := ConfigRoot()
	if err != nil {
		t.Fatal(err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	if root != filepath.Join(home, ".config") {
		t.Fatalf("expected ~/.config on darwin, got %s", root)
	}
	// 明确锁定「不是 Go 的平台惯例」这一点，防止有人改回 os.UserConfigDir()。
	if apple, err := os.UserConfigDir(); err == nil && root == apple {
		t.Fatalf("darwin must not use %s for herdr's socket directory", apple)
	}
}
