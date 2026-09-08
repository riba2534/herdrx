// Package herdrpaths 解析 Herdr 自身在各平台使用的配置目录。
//
// 为什么不直接用 os.UserConfigDir()：在 macOS 上它固定返回
// ~/Library/Application Support 并且完全忽略 XDG_CONFIG_HOME，而 Herdr 实际把
// herdr.sock 放在 ~/.config/herdr/ 下。两者不一致会让受控端在 Mac 上拒绝真实
// socket（"socket path is not allowed"），终端永远打不开。受控端必须按 Herdr
// 的实际位置解析，而不是按 Go 的平台惯例。
package herdrpaths

import (
	"os"
	"path/filepath"
	"runtime"
)

// ConfigRoot 返回存放 herdr 配置目录的父目录。
//
// 解析顺序：
//  1. XDG_CONFIG_HOME（绝对路径时）——Herdr 在所有平台都尊重它；
//  2. macOS 上的 ~/.config——Herdr 的默认位置；
//  3. os.UserConfigDir()——其余平台的既有行为。
func ConfigRoot() (string, error) {
	if dir := os.Getenv("XDG_CONFIG_HOME"); filepath.IsAbs(dir) {
		return filepath.Clean(dir), nil
	}
	if runtime.GOOS == "darwin" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, ".config"), nil
	}
	return os.UserConfigDir()
}

// HerdrDir 返回 Herdr 自己的配置目录，socket 就在其中。
func HerdrDir() (string, error) {
	root, err := ConfigRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "herdr"), nil
}

// CacheRoot 返回缓存根目录，语义与 ConfigRoot 一致。
//
// 受控端的 SSH 分支已经用 ${XDG_CACHE_HOME:-$HOME/.cache} 组装远端暂存目录，
// 而 os.UserCacheDir() 在 macOS 上返回 ~/Library/Caches 且忽略 XDG_CACHE_HOME。
// 两条路径必须落到同一处，否则同一台 Mac 经本机接入和经 SSH 接入会把图片暂存到
// 不同目录。
func CacheRoot() (string, error) {
	if dir := os.Getenv("XDG_CACHE_HOME"); filepath.IsAbs(dir) {
		return filepath.Clean(dir), nil
	}
	if runtime.GOOS == "darwin" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, ".cache"), nil
	}
	return os.UserCacheDir()
}
