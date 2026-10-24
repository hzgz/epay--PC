package main

// 密钥自动提取：调用 wx_key.dll 远程 hook 微信进程，取数据库密钥
//
// wx_key.dll 导出：
//   int  InitializeHook(int pid)          1=成功
//   int  PollKeyData(void* buf, int cap)  返回字节数，>0 表示拿到
//   void CleanupHook()
//
// 注意：hook 只在微信「登录后首次打开数据库」时触发一次。
// 微信已经运行时通常抓不到，需要重启微信后再点一次。

import (
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

var (
	wxkeyOnce  sync.Once
	wxkeyErr   error
	wxkeyInit  *syscall.Proc
	wxkeyPoll  *syscall.Proc
	wxkeyClean *syscall.Proc
	wxkeyMu    sync.Mutex
	wxkeyBusy  bool
)

func wxkeyLoad() error {
	wxkeyOnce.Do(func() {
		dir, err := nativeDir()
		if err != nil {
			wxkeyErr = err
			return
		}
		p := filepath.Join(dir, "wx_key.dll")
		d, err := syscall.LoadDLL(p)
		if err != nil {
			wxkeyErr = fmt.Errorf("加载 wx_key.dll 失败: %w", err)
			return
		}
		var e error
		if wxkeyInit, e = d.FindProc("InitializeHook"); e != nil {
			wxkeyErr = e
			return
		}
		if wxkeyPoll, e = d.FindProc("PollKeyData"); e != nil {
			wxkeyErr = e
			return
		}
		if wxkeyClean, e = d.FindProc("CleanupHook"); e != nil {
			wxkeyErr = e
			return
		}
	})
	return wxkeyErr
}

// KeyResult 密钥提取结果
type KeyResult struct {
	OK    bool   `json:"ok"`
	Key   string `json:"key"`
	Note  string `json:"note"`
	Waits int    `json:"waits"`
}

// ExtractKey 尝试从正在运行的微信进程里取数据库密钥。
// timeout 内轮询；拿不到就返回建议。
func ExtractKey(timeout time.Duration) KeyResult {
	wxkeyMu.Lock()
	if wxkeyBusy {
		wxkeyMu.Unlock()
		return KeyResult{Note: "另一个提取任务正在进行"}
	}
	wxkeyBusy = true
	wxkeyMu.Unlock()
	defer func() {
		wxkeyMu.Lock()
		wxkeyBusy = false
		wxkeyMu.Unlock()
	}()

	if err := wxkeyLoad(); err != nil {
		return KeyResult{Note: err.Error()}
	}

	pid := PidByName(WeChatProc)
	if pid == 0 {
		return KeyResult{Note: "微信没有运行，请先启动微信并登录"}
	}

	// InitializeHook 会向目标进程装远程 hook
	rc, _, _ := wxkeyInit.Call(uintptr(pid))
	if int32(rc) != 1 {
		return KeyResult{Note: fmt.Sprintf("hook 安装失败（返回 %d）", int32(rc))}
	}
	defer wxkeyClean.Call()

	buf := make([]byte, 512)
	deadline := time.Now().Add(timeout)
	waits := 0
	for time.Now().Before(deadline) {
		waits++
		n, _, _ := wxkeyPoll.Call(
			uintptr(unsafe.Pointer(&buf[0])),
			uintptr(len(buf)),
		)
		runtime.KeepAlive(buf)
		cnt := int(int32(n))
		if cnt > 0 && cnt <= len(buf) {
			raw := buf[:cnt]
			// 两种可能：32 字节二进制，或 64 字符 hex
			if k := parseKeyBytes(raw); k != "" {
				return KeyResult{OK: true, Key: k, Waits: waits,
					Note: "已从微信进程取到密钥"}
			}
		}
		time.Sleep(400 * time.Millisecond)
	}
	return KeyResult{
		Waits: waits,
		Note: "微信已经打开过数据库，hook 没有触发。" +
			"请完全退出微信，重新启动并登录后，再点一次「自动获取」。",
	}
}

// parseKeyBytes 把 hook 抓到的缓冲解释成 64 位 hex 密钥
func parseKeyBytes(raw []byte) string {
	// 去掉尾部 NUL
	for len(raw) > 0 && raw[len(raw)-1] == 0 {
		raw = raw[:len(raw)-1]
	}
	if len(raw) == 32 {
		return fmt.Sprintf("%x", raw)
	}
	s := strings.ToLower(strings.TrimSpace(string(raw)))
	if len(s) == 64 {
		for _, c := range s {
			if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
				return ""
			}
		}
		return s
	}
	return ""
}


