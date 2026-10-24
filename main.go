package main

// 易监控PC版 —— 微信 PC 端收款消息监听与上报
//
// 原理：hook Weixin.dll+0x67B7D0（protobuf 序列化），
//       抓取每次序列化的 (buffer, len)，解析出聊天消息。
//       只读，不修改微信逻辑，不碰数据库。

import (
	"fmt"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"
)

const (
	AppName    = "易监控PC版"
	AppVersion = "1.10"
	WeChatProc = "Weixin.exe"
)

type Engine struct {
	mu      sync.Mutex
	hooks    *Hooks
	seen     map[uint64]bool
	history  []MsgView
	debug    []DebugItem
	debugSeq int
	debugged bool
	dbgCount int
	dbgMsg   int
	stopCh   chan struct{}
	stats    Stats
}

type Stats struct {
	InjectedAt time.Time
	Pid        uint32
	Scanned    int
	Parsed     int
	Forwarded  int
	Reported   int
	Failed     int
	LastMsg    string

	// 收款统计（只统计识别出金额的消息）
	TodaySum     float64
	TodayCount   int
	YesterdaySum float64
	YesterdayCnt int
	WeekSum      float64
	WeekCount    int
	AllSum       float64
	AllCount     int
}

var eng = &Engine{seen: make(map[uint64]bool), stopCh: make(chan struct{})}

func logf(format string, a ...interface{}) {
	logL("INFO", "system", format, a...)
}

func (e *Engine) attach() bool {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.hooks != nil {
		return true
	}
	pid := PidByName(WeChatProc)
	if pid == 0 {
		return false
	}
	hk, err := Attach(pid)
	if err != nil {
		logError("inject", "注入失败: %v", err)
		return false
	}
	// 只处理新消息：先丢掉缓冲里残留的旧数据，再记下注入时刻作为下限
	hk.ResetBuffer()
	hk.StartUnix = time.Now().Unix()

	e.hooks = hk
	e.stats.InjectedAt = time.Now()
	e.stats.Pid = pid
	logInfo("inject", "程序启动成功 pid=%d", pid)
	return true
}

func (e *Engine) detach() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.hooks != nil {
		e.hooks.Detach()
		e.hooks = nil
		logInfo("inject", "已卸载 hook")
	}
}

func (e *Engine) poll() {
	e.mu.Lock()
	hk := e.hooks
	e.mu.Unlock()

	if hk == nil {
		return
	}


	for idx := 0; idx < Slots; idx++ {
		_, rdx, r8, _, ok := hk.Slot(idx)
		if !ok || rdx == 0 || r8 == 0 || r8 > 2<<20 {
			continue
		}
		e.stats.Scanned++
		buf := hk.ReadAt(rdx, int(r8))
		if buf == nil {
			continue
		}
		m := ExtractMsg(buf)
		if m == nil {
			if os.Getenv("EASYDEBUG") == "1" && r8 > 600 && r8 < 1200 && e.dbgMsg < 10 {
				e.dbgMsg++
				var fs []PBField
				ParsePB(buf, 0, "", &fs)
				all := make([]string, 0, 20)
				for _, f := range fs {
					if f.Wire == 2 && len(f.Bytes) >= 1 && len(f.Bytes) < 80 {
						s := trimStr(f.Bytes)
						if s != "" {
							all = append(all, f.Path+"="+s)
						}
					}
					if len(all) >= 14 {
						break
					}
				}
				logDebug("monitor", "未知结构 len=%d 字段=%d: %v", r8, len(fs), all)
			}
			continue
		}
		// 只处理注入之后的新消息：早于注入时刻的一律丢弃。
		// 微信的时间字段可能是秒也可能是毫秒，先归一化到秒再比较。
		if hk.StartUnix > 0 && m.Time > 0 {
			ts := int64(m.Time)
			if ts > 1e12 {
				ts /= 1000
			}
			if ts < hk.StartUnix {
				continue
			}
		}

		// 去重：同一条消息在微信内部会被序列化多次（发网络 / 存本地），
		// buffer 里会出现多个副本。MsgID 在不同结构下位置不一致（长路径
		// 的 2.2.2.2.1 是别的计数），所以统一用「发送者+接收者+正文+时间」
		// 做指纹；时间参与哈希，保证不同时刻的相同文案不会被误合并。
		key := hashMsg(m.From+"|"+m.To+"|"+m.Content, m.Time)
		e.mu.Lock()
		if e.seen[key] {
			e.mu.Unlock()
			continue
		}
		if len(e.seen) > 20000 {
			e.seen = make(map[uint64]bool)
		}
		e.seen[key] = true
		e.stats.Parsed++
		e.mu.Unlock()

		e.addDebug(m, len(buf))

		// 收款金额统计（不管是否转发都统计）
		if m.Amount > 0 {
			e.mu.Lock()
			day := msgDay(m.Time)
			now := time.Now()
			today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
			yday := today.AddDate(0, 0, -1)
			week := today.AddDate(0, 0, -6)
			e.stats.AllSum += m.Amount
			e.stats.AllCount++
			if day.Equal(today) {
				e.stats.TodaySum += m.Amount
				e.stats.TodayCount++
			}
			if day.Equal(yday) {
				e.stats.YesterdaySum += m.Amount
				e.stats.YesterdayCnt++
			}
			if !day.Before(week) {
				e.stats.WeekSum += m.Amount
				e.stats.WeekCount++
			}
			e.mu.Unlock()
		}

		// 从收款消息里发现收款账号，自动建档（方便后续绑定通道）
		if m.Amount > 0 && m.To != "" {
			ensureAccount(m.To)
		}

		d := ShouldForward(m)
		if !d.Forward {
			e.addHistory(m, false, "未命中关键词", nil)
			continue
		}
		e.mu.Lock()
		e.stats.Forwarded++
		e.stats.LastMsg = m.Content
		e.mu.Unlock()

		logInfo("monitor", "消息 %s → %s: %s", short(m.From), short(m.To), truncate(m.Content, 60))
		res := Report(m)
		e.mu.Lock()
		if res.Err != "" {
			e.stats.Failed++
			logError("callback", "上报失败: %s", res.Err)
		} else {
			e.stats.Reported++
			logInfo("callback", "上报成功 [%d] %s", res.Code, truncate(res.Body, 40))
		}
		e.mu.Unlock()
		e.addHistory(m, res.Err == "", res.Err, &res)
	}
}

// msgDay 把消息时间戳归一化成当天 0 点（本地时区）
func msgDay(ts uint64) time.Time {
	var t time.Time
	if ts > 1e12 {
		t = time.UnixMilli(int64(ts))
	} else if ts > 1e9 {
		t = time.Unix(int64(ts), 0)
	} else {
		t = time.Now()
	}
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, t.Location())
}

func hashMsg(s string, t uint64) uint64 {
	var h uint64 = 1469598103934665603
	for i := 0; i < len(s); i++ {
		h ^= uint64(s[i])
		h *= 1099511628211
	}
	h ^= t
	return h
}

func short(s string) string {
	if len(s) > 14 {
		return s[:14]
	}
	return s
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

func (e *Engine) run() {
	tick := time.NewTicker(120 * time.Millisecond)
	defer tick.Stop()
	recheck := time.NewTicker(3 * time.Second)
	defer recheck.Stop()

	for {
		select {
		case <-e.stopCh:
			return
		case <-recheck.C:
			if e.hooks != nil {
				if PidByName(WeChatProc) != e.stats.Pid {
					logWarn("inject", "微信进程已变化，重新注入")
					e.detach()
				}
			} else {
				if GetConfig().AutoInject && e.attach() {
					continue
				}
			}
		case <-tick.C:
			e.poll()
		}
	}
}

func main() {
	fmt.Printf("%s v%s\n", AppName, AppVersion)
	fmt.Println("─────────────────────────────────────────")

	if err := LoadConfig(); err != nil {
		logError("config", "配置加载失败: %v", err)
	}
	c := GetConfig()
	mode := map[string]string{"yjk": "易简控", "v1": "码支付V1", "none": "仅打印"}[c.Mode]
	filterMode := "全量转发"
	if !c.ForwardAll {
		filterMode = "关键词: " + fmt.Sprint(c.Keywords)
	}
	logInfo("config", "上报模式: %s   过滤: %s", mode, filterMode)
	logInfo("config", "配置文件: %s", configPath())
	loadAccounts()
	logInfo("config", "账号绑定: %d 个", len(accountsCopy()))

	if PidByName(WeChatProc) == 0 {
		logWarn("monitor", "未检测到 %s，等待启动…", WeChatProc)
	}

	go eng.run()

	// 启动界面
	port, err := startServer()
	if err != nil {
		logError("system", "界面服务启动失败: %v", err)
	} else {
		logInfo("system", "界面: http://127.0.0.1:%d/", port)
	}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	statusTk := time.NewTicker(30 * time.Second)
	defer statusTk.Stop()

	// 顶层窗口用 WebView2 承载（原生窗口，不是浏览器）
	go func() {
		for {
			select {
			case <-sig:
				logInfo("system", "正在退出…")
				close(eng.stopCh)
				eng.detach()
				os.Exit(0)
			case <-statusTk.C:
				eng.mu.Lock()
				s := eng.stats
				attached := eng.hooks != nil
				eng.mu.Unlock()
				if attached {
					logDebug("monitor", "运行中 扫描=%d 消息=%d 转发=%d 上报=%d 失败=%d",
						s.Scanned, s.Parsed, s.Forwarded, s.Reported, s.Failed)
				} else {
					logDebug("monitor", "状态: 等待微信进程…")
				}
			}
		}
	}()

	if port == 0 {
		// 端口没起来就退回终端模式
		select {}
	}
	runWindow(port)
}

















