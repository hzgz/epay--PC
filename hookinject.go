package main

// Hook DLL 的注入与看护：
//   - 启用 Hook 模式时，把 native\easymon_hook*.dll 注入当前微信主进程；
//   - 微信重启（新 PID）后自动重新注入；
//   - 注入失败只记日志，不影响程序其它功能。

import (
	"fmt"
	"io"
	"unsafe"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
)

var (
	hookWatchMu   sync.Mutex
	hookWatchStop chan struct{}
	injectedPid   uint32
)

// hookDLLPath 选一个存在的 DLL（优先 easymon_hook.dll，其次带版本后缀的）
func hookDLLPath() string {
	exe, err := os.Executable()
	base := "."
	if err == nil {
		base = filepath.Dir(exe)
	}
	cands := []string{
		filepath.Join(base, "native", "easymon_hook.dll"),
		filepath.Join(base, "native", "easymon_hook3.dll"),
		filepath.Join(base, "native", "easymon_hook2.dll"),
	}
	// 取最新的一个：换版本时直接丢新文件进 native 目录即可，不用改名
	best := ""
	var bestTime time.Time
	for _, p := range cands {
		st, err := os.Stat(p)
		if err != nil || st.Size() == 0 {
			continue
		}
		if best == "" || st.ModTime().After(bestTime) {
			best, bestTime = p, st.ModTime()
		}
	}
	return best
}

// injectDLL 用 LoadLibraryW 远程线程把 DLL 注入目标进程
func injectDLL(pid uint32, dllPath string) error {
	k32 := windows.NewLazySystemDLL("kernel32.dll")
	pOpen := k32.NewProc("OpenProcess")
	pAlloc := k32.NewProc("VirtualAllocEx")
	pWrite := k32.NewProc("WriteProcessMemory")
	pThread := k32.NewProc("CreateRemoteThread")
	pWait := k32.NewProc("WaitForSingleObject")
	pFree := k32.NewProc("VirtualFreeEx")
	pClose := k32.NewProc("CloseHandle")
	pLoadLib := k32.NewProc("LoadLibraryW")
	pGetExit := k32.NewProc("GetExitCodeThread")

	const (
		processAllAccess = 0x1F0FFF
		memCommit        = 0x1000
		memReserve       = 0x2000
		memRelease       = 0x8000
		pageReadWrite    = 0x04
	)

	h, _, err := pOpen.Call(processAllAccess, 0, uintptr(pid))
	if h == 0 {
		return fmt.Errorf("OpenProcess: %v", err)
	}
	defer pClose.Call(h)

	path, err := syscall.UTF16FromString(dllPath)
	if err != nil {
		return err
	}
	size := uintptr(len(path) * 2)
	remote, _, err := pAlloc.Call(h, 0, size, memCommit|memReserve, pageReadWrite)
	if remote == 0 {
		return fmt.Errorf("VirtualAllocEx: %v", err)
	}
	defer pFree.Call(h, remote, 0, memRelease)

	var written uintptr
	ok, _, err := pWrite.Call(h, remote, uintptr(unsafe.Pointer(&path[0])), size,
		uintptr(unsafe.Pointer(&written)))
	if ok == 0 {
		return fmt.Errorf("WriteProcessMemory: %v", err)
	}

	th, _, err := pThread.Call(h, 0, 0, pLoadLib.Addr(), remote, 0, 0)
	if th == 0 {
		return fmt.Errorf("CreateRemoteThread: %v", err)
	}
	defer pClose.Call(th)

	pWait.Call(th, 6000)
	var code uint32
	pGetExit.Call(th, uintptr(unsafe.Pointer(&code)))
	if code == 0 {
		return fmt.Errorf("LoadLibraryW 返回 0（注入被拒或路径不可达）")
	}
	return nil
}

// startHookWatcher 看护微信进程：没有注入就注入，换进程就重注
func startHookWatcher() {
	hookWatchMu.Lock()
	if hookWatchStop != nil {
		hookWatchMu.Unlock()
		return
	}
	stop := make(chan struct{})
	hookWatchStop = stop
	hookWatchMu.Unlock()

	go func() {
		tick := time.NewTicker(5 * time.Second)
		defer tick.Stop()
		for {
			select {
			case <-stop:
				return
			case <-tick.C:
				if GetConfig().EffectiveWxMode() != "hook" {
					continue
				}
				pids := MainPidsOf(WeChatProc)
				if len(pids) == 0 {
					continue
				}
				pid := pids[0]
				if pid == injectedPid {
					continue
				}
				dll := hookDLLPath()
				if dll == "" {
					logWarn("hook", "缺少消息组件，请重新安装本程序")
					continue
				}
				// 已经加载过就别重复注入（重复注入不会重跑 DllMain，也不会重新挂钩）
				if moduleLoaded(pid, "easymon_hook") {
					injectedPid = pid
					ensureHookAccountForPid(pid) // 账号去重/改名仍要跑
					continue
				}
				before := hookLogSize()
				if err := injectDLL(pid, dll); err != nil {
					logError("hook", "接入微信失败（%d）：%v", pid, err)
					continue
				}
				// 注入成功 ≠ 挂钩成功：微信升级后序言可能变了，DLL 会拒绝挂钩。
				// 这里看 DLL 日志确认真的挂上了，避免界面显示"已连接"其实收不到消息。
				time.Sleep(600 * time.Millisecond)
				if !hookLogHasSince(before, "hook 已安装") {
					if hookLogHasSince(before, "序言不匹配") {
						logError("hook", "消息组件未就绪：微信版本可能已更新（%d）", pid)
					} else {
						logWarn("hook", "消息组件尚未确认就绪（%d）", pid)
					}
				}
				injectedPid = pid
				logInfo("hook", "已连接微信进程 %d", pid)
				ensureHookAccountForPid(pid)
			}
		}
	}()
}

// ensureHookAccountForPid 微信换了进程后，把 Hook 账号自动换成新 PID，
// 并清掉已经不存在的旧 PID 账号，避免账号列表越积越多。
// singleWxidFromDisk 从微信数据目录推出当前登录的 wxid。
// 目录名形如 wxid_xxx_5d71，活跃账号的 db_storage 最近被写过，按修改时间取最新的那个。
func singleWxidFromDisk() string {
	root := WeChatRoot()
	if root == "" {
		return ""
	}
	ents, err := os.ReadDir(root)
	if err != nil {
		return ""
	}
	strip := func(name string) string {
		if i := strings.LastIndex(name, "_"); i > 0 && len(name)-i == 5 {
			return name[:i]
		}
		return name
	}
	type cand struct {
		name string
		mod  time.Time
	}
	var list []cand
	for _, e := range ents {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), "wxid_") {
			continue
		}
		// 活跃账号的消息库一直在写，用它的修改时间判断
		dbFile := filepath.Join(root, e.Name(), "db_storage", "message", "message_0.db")
		st, err := os.Stat(dbFile)
		if err != nil {
			dbFile = filepath.Join(root, e.Name(), "db_storage")
			st, err = os.Stat(dbFile)
		}
		if err != nil {
			continue
		}
		list = append(list, cand{name: strip(e.Name()), mod: st.ModTime()})
	}
	if len(list) == 0 {
		return ""
	}
	best := list[0]
	for _, c := range list[1:] {
		if c.mod.After(best.mod) {
			best = c
		}
	}
	return best.name
}

func ensureHookAccountForPid(pid uint32) {
	pidInt := int(pid)
	c := GetConfig()
	accounts := c.HookAccounts
	changed := false
	kept := make([]HookAccount, 0, len(accounts)+1)
	has := false
	for _, a := range accounts {
		if a.PID == pidInt {
			has = true
			kept = append(kept, a)
			continue
		}
		if a.PID != 0 && len(PidsByName(WeChatProc)) > 0 {
			if processAlive(uint32(a.PID)) {
				kept = append(kept, a)
				continue
			}
		}
		changed = true // 旧进程已退出，丢弃
	}
	// 账号名尽量用真实 wxid，界面上"接收者"才不会显示成 pid:xxxx
	if wx := singleWxidFromDisk(); wx != "" {
		for i := range kept {
			if kept[i].PID == pidInt && kept[i].Wxid != wx {
				kept[i].Wxid = wx
				changed = true
			}
		}
	}
	if !has {
		// 用户主动删光了账号就不再加回来，否则账号删不干净、也切不回密钥模式
		if c.HookNoAutoBind && len(kept) == 0 {
			return
		}
		// 新建账号时继承任意已有账号的通道号，避免换进程后通道丢失
		ch := ""
		for _, a := range append(kept, accounts...) {
			if a.ChannelID != "" {
				ch = a.ChannelID
				break
			}
		}
		name := singleWxidFromDisk()
		if name == "" {
			name = fmt.Sprintf("pid:%d", pid)
		}
		kept = append(kept, HookAccount{
			Wxid: name, PID: pidInt, ChannelID: ch, Enabled: true,
		})
		changed = true
		logInfo("hook", "已自动绑定当前微信进程 pid=%d", pid)
	}
	// 同一 PID 只留一个账号
	dedup := make([]HookAccount, 0, len(kept))
	seen := map[int]bool{}
	for _, a := range kept {
		if a.PID != 0 && seen[a.PID] {
			continue
		}
		seen[a.PID] = true
		dedup = append(dedup, a)
	}
	if len(dedup) != len(kept) {
		kept = dedup
		changed = true
	}
	if changed {
		c.HookAccounts = kept
		if err := SetConfig(c); err != nil {
			logWarn("hook", "更新账号失败：%v", err)
		}
		// 账号刚就绪就发一轮心跳，不等 60 秒周期
		if strings.TrimSpace(c.Mode) == "xpay" {
			ensureXPayHeartbeat()
			go xpayHeartbeatRound()
		}
	}
}

func processAlive(pid uint32) bool {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return false
	}
	defer windows.CloseHandle(h)
	var code uint32
	if err := windows.GetExitCodeProcess(h, &code); err != nil {
		return false
	}
	return code == 259 // STILL_ACTIVE
}

// stopHookWatcher 停止看护
func stopHookWatcher() {
	hookWatchMu.Lock()
	if hookWatchStop != nil {
		close(hookWatchStop)
		hookWatchStop = nil
	}
	injectedPid = 0
	hookWatchMu.Unlock()
}

// moduleLoaded 目标进程里是否已加载某个名字的模块（按前缀匹配，兼容带版本后缀的 dll）
func moduleLoaded(pid uint32, prefix string) bool {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPMODULE|windows.TH32CS_SNAPMODULE32, pid)
	if err != nil {
		return false
	}
	defer windows.CloseHandle(snap)
	var me windows.ModuleEntry32
	me.Size = uint32(unsafe.Sizeof(me))
	if err := windows.Module32First(snap, &me); err != nil {
		return false
	}
	for {
		name := strings.ToLower(windows.UTF16ToString(me.Module[:]))
		if strings.HasPrefix(name, prefix) {
			return true
		}
		if err := windows.Module32Next(snap, &me); err != nil {
			return false
		}
	}
}

// hookLogSize / hookLogHasSince 用 DLL 日志判断挂钩是否成功
func hookLogPath() string { return filepath.Join(os.TempDir(), "easymon_hook.log") }

func hookLogSize() int64 {
	st, err := os.Stat(hookLogPath())
	if err != nil {
		return 0
	}
	return st.Size()
}

func hookLogHasSince(offset int64, needle string) bool {
	f, err := os.Open(hookLogPath())
	if err != nil {
		return false
	}
	defer f.Close()
	if offset > 0 {
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			return false
		}
	}
	b, err := io.ReadAll(f)
	if err != nil {
		return false
	}
	return strings.Contains(string(b), needle)
}

// HookDLLInfo 供界面显示
func HookDLLInfo() map[string]any {
	p := hookDLLPath()
	name := ""
	if p != "" {
		name = filepath.Base(p)
	}
	return map[string]any{
		"path": p, "name": name,
		"injected_pid": injectedPid,
		"exists":       strings.TrimSpace(p) != "",
	}
}
