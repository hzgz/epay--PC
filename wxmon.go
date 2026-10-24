package main

// 数据库轮询：监听公众号新消息并触发上报
//
// 与 hook 方案互补：
//   hook   → 抓聊天消息（一对一/群聊）
//   数据库 → 抓公众号/服务号消息（微信收款助手、微信支付、赞赏）

import (
	"os"
	"strings"
	"sync"
	"time"
)

// WxWatch 一个被监听的公众号
type WxWatch struct {
	AccountID string // 微信账号 wxid
	GhID      string // 公众号 gh_xxx
	Name      string // 显示名
	Table     string // Msg_<md5>
	LastID    int64  // 已处理的最大 local_id
}

// WxMonitor 数据库监控器
type WxMonitor struct {
	mu      sync.Mutex
	db      *WxDB
	acc     WxAccount
	watches map[string]*WxWatch // table -> watch
	started bool
	stop    chan struct{}
	// 首次读取时只记录位置，不上报历史
	primed map[string]bool
	// 每个表最近一次读取时间
	lastPoll time.Time

	// 轮询统计（给首页展示）
	pollRounds int   // 完成轮询的轮数
	pollMsgs   int   // 累计读到的消息条数
	pollStart  time.Time
}

var wxMon *WxMonitor

// StartWxMonitor 启动数据库监控
func StartWxMonitor(dbPath, keyHex, wxid string, acc WxAccount) error {
	db, err := OpenWxDB(dbPath, keyHex)
	if err != nil {
		return err
	}
	m := &WxMonitor{
		db:      db,
		acc:     acc,
		watches: map[string]*WxWatch{},
		stop:    make(chan struct{}),
		primed:  map[string]bool{},
	}
	// 自动发现所有 gh_ 表
	tables, err := db.Tables()
	if err != nil {
		db.Close()
		return err
	}
	for _, t := range tables {
		if !strings.HasPrefix(t, "Msg_") {
			continue
		}
		m.watches[t] = &WxWatch{AccountID: wxid, Table: t}
	}
	m.started = true
	m.pollStart = time.Now()
	wxMon = m
	go m.loop()
	logInfo("monitor", "数据库监控已启动：%d 个公众号表", len(m.watches))
	return nil
}

// StopWxMonitor 停止
func StopWxMonitor() {
	m := wxMon
	if m == nil {
		return
	}
	select {
	case <-m.stop:
	default:
		close(m.stop)
	}
	if m.db != nil {
		m.db.Close()
	}
	wxMon = nil
	logInfo("monitor", "数据库监控已停止")
}

// BindGh 绑定一个要监听的公众号
func (m *WxMonitor) BindGh(ghID, name string) {
	tbl := GhTable(ghID)
	m.mu.Lock()
	defer m.mu.Unlock()
	w, ok := m.watches[tbl]
	if !ok {
		w = &WxWatch{AccountID: m.acc.Wxid, Table: tbl}
		m.watches[tbl] = w
	}
	w.GhID = ghID
	w.Name = name
	logInfo("monitor", "已绑定公众号 %s (%s) → %s", name, ghID, tbl)
}

// loop 主循环
func (m *WxMonitor) loop() {
	t := time.NewTicker(3 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-m.stop:
			return
		case <-t.C:
			m.pollOnce()
		}
	}
}

// pollOnce 读一轮新消息
func (m *WxMonitor) pollOnce() {
	m.mu.Lock()
	list := make([]*WxWatch, 0, len(m.watches))
	for _, w := range m.watches {
		if w.GhID != "" {
			list = append(list, w)
		}
	}
	m.mu.Unlock()
	if len(list) == 0 {
		return
	}

	m.mu.Lock()
	m.pollRounds++
	m.mu.Unlock()

	for _, w := range list {
		msgs, err := m.db.Messages(w.Table, 20, 0, false)
		if err != nil {
			logWarn("monitor", "%s 读取失败: %v", w.Name, err)
			continue
		}
		m.mu.Lock()
		m.pollMsgs += len(msgs)
		primed := m.primed[w.Table]
		m.mu.Unlock()
		if !primed {
			var maxID int64
			for _, x := range msgs {
				if x.LocalID > maxID {
					maxID = x.LocalID
				}
			}
			m.mu.Lock()
			w.LastID = maxID
			m.primed[w.Table] = true
			m.mu.Unlock()
			logInfo("monitor", "%s 已就位（跳过历史 %d 条，游标 local_id=%d）", w.Name, len(msgs), maxID)
			continue
		}

		// 只处理比游标新的，按时间正序处理
		fresh := make([]RawMsg, 0, len(msgs))
		for _, x := range msgs {
			if x.LocalID > w.LastID {
				fresh = append(fresh, x)
			}
		}
		if len(fresh) == 0 {
			continue
		}
		// 升序
		for i := 0; i < len(fresh); i++ {
			for j := i + 1; j < len(fresh); j++ {
				if fresh[j].LocalID < fresh[i].LocalID {
					fresh[i], fresh[j] = fresh[j], fresh[i]
				}
			}
		}
		for _, x := range fresh {
			m.handle(w, x)
			m.mu.Lock()
			if x.LocalID > w.LastID {
				w.LastID = x.LocalID
			}
			m.mu.Unlock()
		}
	}
}

// handle 处理一条新消息
func (m *WxMonitor) handle(w *WxWatch, raw RawMsg) {
	gm := ParseGhContent(raw.Content)
	if gm.Title == "" {
		return
	}

	// 组装成与 hook 一致的 ChatMsg
	m2 := &ChatMsg{
		From:    w.GhID,
		To:      m.acc.Wxid,
		Content: gm.Title,
		Display: gm.Title,
		Time:    uint64(raw.CreateAt),
		RawText: gm.Decoded, // 完整 XML
		Amount:  gm.Amount,
		MsgID:   uint64(raw.ServerID),
		SortSeq: raw.SortSeq,
		LocalID: raw.LocalID,
		GHFrom:  w.GhID,
		GHName:  w.Name,
	}

	// 转发规则（与 hook 路径共用同一套判断）
	if d := ShouldForward(m2); !d.Forward {
		logDebug("monitor", "跳过: %s", d.Reason)
		return
	}

	// 去重
	key := hashMsg(m2.From+"|"+m2.To+"|"+m2.Content, m2.Time)
	eng.mu.Lock()
	if eng.seen[key] {
		eng.mu.Unlock()
		return
	}
	eng.seen[key] = true
	eng.stats.Forwarded++
	eng.mu.Unlock()

	res := Report(m2)
	reportOK := res.Delivered && res.OK
	eng.mu.Lock()
	if res.Delivered && !res.OK {
		eng.stats.Failed++
		if res.Err == "" {
			res.Err = "HTTP " + itoa(int(res.Code))
		}
		logError("callback", "数据库消息上报失败: %s", res.Err)
	} else if reportOK {
		eng.stats.Reported++
		logInfo("callback", "数据库消息上报成功 [%d]", res.Code)
	}
	eng.mu.Unlock()

	eng.addHistory(m2, reportOK, res.Err, &res)
	logInfo("monitor", "[%s] %s  ¥%.2f", w.Name, truncate(gm.Title, 40), m2.Amount)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

// WxMonitorStatus 监控状态（给 UI）
type WxMonitorStatus struct {
	Running  bool
	Wxid     string
	Watches  []WxWatchStatus
	PollRounds int
	PollMsgs   int
	UptimeSec  int64
}

type WxWatchStatus struct {
	Name   string
	GhID   string
	Table  string
	LastID int64
	Primed bool
}

// WxStatus 取状态
func WxStatus() WxMonitorStatus {
	m := wxMon
	if m == nil {
		return WxMonitorStatus{}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	st := WxMonitorStatus{
		Running:    m.started,
		Wxid:       m.acc.Wxid,
		PollRounds: m.pollRounds,
		PollMsgs:   m.pollMsgs,
	}
	if !m.pollStart.IsZero() {
		st.UptimeSec = int64(time.Since(m.pollStart).Seconds())
	}
	for _, w := range m.watches {
		if w.GhID == "" {
			continue
		}
		st.Watches = append(st.Watches, WxWatchStatus{
			Name: w.Name, GhID: w.GhID, Table: w.Table,
			LastID: w.LastID, Primed: m.primed[w.Table],
		})
	}
	return st
}

// DefaultWatches 默认监听的公众号
var defaultWatches = []struct{ Gh, Name string }{
	{"gh_f0a92aa7146c", "微信收款助手"},
	{"gh_3dfda90e39d6", "微信支付"},
}

// InitDefaultWatches 绑定默认公众号
func InitDefaultWatches() {
	m := wxMon
	if m == nil {
		return
	}
	for _, d := range defaultWatches {
		m.BindGh(d.Gh, d.Name)
	}
}

var _ = os.Stat





