package main

// 密钥自动获取 · 等待模式
//
// wx_key.dll 的 hook 只在微信「登录后首次打开数据库」时触发一次。
// 微信已经在运行时抓不到，所以这里盯着进程：一旦发现微信重新启动，
// 立刻装 hook，然后轮询，抓到就保存。

import (
	"sync"
	"time"
	"unsafe"
)

var (
	keyWatchMu     sync.Mutex
	keyWatchActive bool
	keyWatchResult KeyResult
	keyWatchState  string // 给 UI 看的状态文案
)

// 等待模式下当前状态
type KeyWatchStatus struct {
	Active bool   `json:"active"`
	State  string `json:"state"`
	Key    string `json:"key"`
	OK     bool   `json:"ok"`
}

// KeyWatchStatusNow 查询等待模式状态
func KeyWatchStatusNow() KeyWatchStatus {
	keyWatchMu.Lock()
	defer keyWatchMu.Unlock()
	return KeyWatchStatus{
		Active: keyWatchActive,
		State:  keyWatchState,
		Key:    keyWatchResult.Key,
		OK:     keyWatchResult.OK,
	}
}

// StartKeyWatch 启动等待模式：盯微信重启 → 装 hook → 抓密钥
func StartKeyWatch(timeout time.Duration) {
	keyWatchMu.Lock()
	if keyWatchActive {
		keyWatchMu.Unlock()
		return
	}
	keyWatchActive = true
	keyWatchState = "等待微信重启…请先完全退出微信"
	keyWatchResult = KeyResult{}
	keyWatchMu.Unlock()

	go keyWatchLoop(timeout)
}

func keyWatchLoop(timeout time.Duration) {
	defer func() {
		keyWatchMu.Lock()
		keyWatchActive = false
		keyWatchMu.Unlock()
	}()

	if err := wxkeyLoad(); err != nil {
		setKeyWatchState("wx_key.dll 加载失败：" + err.Error(), KeyResult{Note: err.Error()})
		return
	}

	lastPid := PidByName(WeChatProc)
	var hookedPid uint32
	deadline := time.Now().Add(timeout)
	buf := make([]byte, 512)
	waits := 0

	for time.Now().Before(deadline) {
		pid := PidByName(WeChatProc)

		// 微信没跑：等待
		if pid == 0 {
			if lastPid != 0 {
				setKeyWatchState("微信已退出，等待重新启动…", KeyResult{})
			}
			lastPid = 0
			time.Sleep(400 * time.Millisecond)
			continue
		}

		// 新进程：立刻装 hook
		if pid != hookedPid {
			if lastPid == 0 {
				setKeyWatchState("检测到微信已启动，正在装 hook…", KeyResult{})
			}
			rc, _, _ := wxkeyInit.Call(uintptr(pid))
			if int32(rc) == 1 {
				hookedPid = pid
				setKeyWatchState("hook 已就位，请登录微信", KeyResult{})
			} else {
				// 装不上就等下一轮
				time.Sleep(300 * time.Millisecond)
				lastPid = pid
				continue
			}
		}
		lastPid = pid

		// 轮询密钥
		waits++
		n, _, _ := wxkeyPoll.Call(
			uintptr(unsafe.Pointer(&buf[0])),
			uintptr(len(buf)),
		)
		cnt := int(int32(n))
		if cnt > 0 && cnt <= len(buf) {
			if k := parseKeyBytes(buf[:cnt]); k != "" {
				wxkeyClean.Call()
				res := KeyResult{OK: true, Key: k, Waits: waits, Note: "已自动获取密钥"}
				setKeyWatchState("已获取密钥", res)
				// 落到配置里，直接可用来开库
				cfg := GetConfig()
				cfg.DbKey = k
				SetConfig(cfg)
				return
			}
		}
		time.Sleep(350 * time.Millisecond)
	}

	wxkeyClean.Call()
	setKeyWatchState("超时未取到密钥，请再试一次", KeyResult{
		Note: "超时未取到密钥。确认微信已完全退出并重新启动登录。",
	})
}

func setKeyWatchState(state string, res KeyResult) {
	keyWatchMu.Lock()
	keyWatchState = state
	if res.Key != "" || res.Note != "" {
		keyWatchResult = res
	}
	keyWatchMu.Unlock()
}

