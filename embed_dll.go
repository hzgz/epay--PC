package main

// native DLL 打包进 exe，首次运行时释放到用户目录。
// 这样分发只有一个 exe，不用单独放置 DLL。

import (
	"crypto/sha256"
	"embed"
	"fmt"
	"os"
	"path/filepath"
)

//go:embed native/WCDB.dll
//go:embed native/wxtool_wcdb.dll
//go:embed native/wx_key.dll
var nativeFS embed.FS

var nativeDirCached string

// nativeDir 返回 DLL 释放目录，必要时先释放
func nativeDir() (string, error) {
	if nativeDirCached != "" {
		return nativeDirCached, nil
	}

	// 优先用 exe 旁边的 native 目录（开发时直接用，不重复释放）
	exe, err := os.Executable()
	if err == nil {
		adj := filepath.Join(filepath.Dir(exe), "native")
		if _, e := os.Stat(filepath.Join(adj, "WCDB.dll")); e == nil {
			nativeDirCached = adj
			return adj, nil
		}
	}

	base, err := os.UserCacheDir()
	if err != nil || base == "" {
		base = os.TempDir()
	}
	dst := filepath.Join(base, "EasyMonitorPC", "native")
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return "", fmt.Errorf("创建 DLL 目录失败: %w", err)
	}

	for _, name := range []string{"WCDB.dll", "wxtool_wcdb.dll", "wx_key.dll"} {
		data, err := nativeFS.ReadFile("native/" + name)
		if err != nil {
			return "", fmt.Errorf("内置资源缺失 %s: %w", name, err)
		}
		target := filepath.Join(dst, name)
		// 内容一致就跳过，避免每次启动都写盘
		if sameFile(target, data) {
			continue
		}
		tmp := target + ".tmp"
		if err := os.WriteFile(tmp, data, 0o644); err != nil {
			return "", fmt.Errorf("释放 %s 失败: %w", name, err)
		}
		// 目标可能被占用（程序运行中），失败就沿用旧文件
		if err := os.Rename(tmp, target); err != nil {
			os.Remove(tmp)
			if _, e := os.Stat(target); e != nil {
				return "", fmt.Errorf("写入 %s 失败: %w", name, err)
			}
		}
	}

	nativeDirCached = dst
	return dst, nil
}

func sameFile(path string, want []byte) bool {
	old, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	if len(old) != len(want) {
		return false
	}
	return sha256.Sum256(old) == sha256.Sum256(want)
}
