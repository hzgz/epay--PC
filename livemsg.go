package main

// 消息调试的白名单与好友/群聊实时轮询。
//
// 微信数据库里 biz_message_0.db 有几十上百个公众号表，绝大多数是营销推送，
// 调试时全列出来没意义。只保留真正的收款渠道 + 好友/群聊。
//
// 已确认的公众号（从消息 XML 的 <username> 字段读出）：
//   微信收款助手        gh_f0a92aa7146c
//   微信支付            wxzhifu
//   微信收款商业版      wxmaidan
//   微信支付商家助手    WXPayMchPlat

import (
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// debugAllowedNicks 调试里允许显示的公众号昵称
var debugAllowedNicks = []string{
	"微信收款助手",
	"微信支付",
	"微信支付商家助手",
	"微信收款商业版",
}

// debugAllowedGH 已知的 gh_id（备用匹配，昵称取不到时用）
var debugAllowedGH = map[string]bool{
	"gh_f0a92aa7146c": true, // 微信收款助手
	"wxzhifu":         true, // 微信支付
	"wxmaidan":        true, // 微信收款商业版
	"WXPayMchPlat":    true, // 微信支付商家助手
}

var (
	reGhNick   = regexp.MustCompile(`<nickname><!\[CDATA\[([^\]]+)\]\]>`)
	reGhUser   = regexp.MustCompile(`<username><!\[CDATA\[([^\]]+)\]\]>`)
	reGhName2  = regexp.MustCompile(`<nickname>([^<]+)</nickname>`)
	reGhUser2  = regexp.MustCompile(`<username>([^<]+)</username>`)
)

// ghIdentity 从一条公众号消息里认出公众号身份
func ghIdentity(content string) (nick, gh string) {
	if content == "" {
		return "", ""
	}
	if m := reGhNick.FindStringSubmatch(content); len(m) > 1 {
		nick = strings.TrimSpace(m[1])
	}
	if m := reGhUser.FindStringSubmatch(content); len(m) > 1 {
		gh = strings.TrimSpace(m[1])
	}
	if nick == "" {
		if m := reGhName2.FindStringSubmatch(content); len(m) > 1 {
			nick = strings.TrimSpace(m[1])
		}
	}
	if gh == "" {
		if m := reGhUser2.FindStringSubmatch(content); len(m) > 1 {
			gh = strings.TrimSpace(m[1])
		}
	}
	return nick, gh
}

// isAllowedGh 这个公众号该不该出现在消息调试里
func isAllowedGh(nick, gh string) bool {
	for _, n := range debugAllowedNicks {
		if nick != "" && strings.Contains(nick, n) {
			return true
		}
	}
	if gh != "" && debugAllowedGH[gh] {
		return true
	}
	return false
}

// ---------------------------------------------------------------------------
// 好友 / 群聊实时轮询
// ---------------------------------------------------------------------------

// liveMsg 一条实时消息（好友、群聊或收款公众号）
type liveMsg struct {
	Table   string
	From    string
	Preview string
	Time    int64
	LocalID int64
	IsGh    bool
}

// LiveMonitor 轮询 message_0.db，把新消息推给「消息调试」
type LiveMonitor struct {
	mu      sync.Mutex
	running bool
	stop    chan struct{}
	// 每张表最近处理过的 local_id
	last map[string]int64
	// 最近的实时消息（环形缓冲）
	events []liveMsg
	// 首次采样时只记游标，不推消息
	primed bool
	db     *WxDB
	wxid   string
	pushed int64
}

var (
	liveMonMu sync.Mutex
	liveMons  = map[string]*LiveMonitor{} // wxid -> monitor
)

const liveKeep = 20

// StartLiveMonitor 开始轮询某个账号的 message_0.db
func StartLiveMonitor(wxid, dbPath, keyHex string) error {
	liveMonMu.Lock()
	if old := liveMons[wxid]; old != nil {
		liveMonMu.Unlock()
		old.Stop()
		liveMonMu.Lock()
	}
	liveMonMu.Unlock()

	db, err := OpenWxDB(dbPath, keyHex)
	if err != nil {
		return err
	}
	m := &LiveMonitor{
		stop: make(chan struct{}),
		last: map[string]int64{},
		db:   db,
		wxid: wxid,
	}
	liveMonMu.Lock()
	liveMons[wxid] = m
	liveMonMu.Unlock()

	m.running = true
	go m.loop()
	logInfo("monitor", "账号 %s 好友/群聊实时监控已启动", wxid)
	return nil
}

// StopLiveMonitor 停止某个账号的实时监控
func StopLiveMonitor(wxid string) {
	liveMonMu.Lock()
	m := liveMons[wxid]
	delete(liveMons, wxid)
	liveMonMu.Unlock()
	if m != nil {
		m.Stop()
		logInfo("monitor", "账号 %s 好友/群聊实时监控已停止", wxid)
	}
}

// Stop 内部停止
func (m *LiveMonitor) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.running {
		return
	}
	m.running = false
	select {
	case <-m.stop:
	default:
		close(m.stop)
	}
	if m.db != nil {
		m.db.Close()
	}
}

func (m *LiveMonitor) loop() {
	// 5 秒一轮：message_0.db 有一百多张表，全扫一次要 1-2 秒，
	// 再快会挤占资源
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	m.pollOnce() // 首次立刻采样
	for {
		select {
		case <-m.stop:
			return
		case <-t.C:
			m.pollOnce()
		}
	}
}

func (m *LiveMonitor) pollOnce() {
	if m.db == nil {
		return
	}
	tables, err := m.db.Tables()
	if err != nil {
		return
	}
	var fresh []liveMsg
	primed := m.primed

	for _, t := range tables {
		if !strings.HasPrefix(t, "Msg_") {
			continue
		}
		msgs, err := m.db.Messages(t, 5, 0, false) // 最新 5 条
		if err != nil || len(msgs) == 0 {
			continue
		}
		// 表内按 local_id 正序处理
		for i := len(msgs) - 1; i >= 0; i-- {
			raw := msgs[i]
			if raw.LocalID <= m.last[t] {
				continue
			}
			m.last[t] = raw.LocalID
			if primed {
				continue // 首次只记游标
			}
			txt, _, _ := DecodeContent(raw.Content)
			from := ""
			if i := strings.Index(txt, ":"); i > 0 && i < 64 {
				p := txt[:i]
				if strings.Contains(p, "@") || strings.HasPrefix(p, "wxid_") {
					from = p
					txt = strings.TrimSpace(txt[i+1:])
				}
			}
			if from == "" {
				if p := guessPeerFromMsg(txt, m.wxid); p != "" {
					from = p
					// 顺手把这张表的映射记下来，下次直接用
					tm := loadTableMap(m.wxid)
					if _, ok := tm[t]; !ok {
						tm[t] = p
						saveTableMap(m.wxid, tm)
					}
				} else if v, ok := loadTableMap(m.wxid)[t]; ok {
					from = v
				}
			}
			if from == m.wxid {
				from = "我"
			}
			if from == "" {
				from = t
			}
			prev := txt
			if strings.HasPrefix(strings.TrimSpace(prev), "<") {
				switch {
				case strings.Contains(prev, "<img"):
					prev = "[图片]"
				case strings.Contains(prev, "<videomsg"):
					prev = "[视频]"
				case strings.Contains(prev, "<emoji"):
					prev = "[表情]"
				case strings.Contains(prev, "<voicemsg"):
					prev = "[语音]"
				case strings.Contains(prev, "revokemsg"):
					prev = "[撤回]"
				case strings.Contains(prev, "sysmsgtemplate"):
					prev = "[系统消息]"
				case strings.Contains(prev, "<appmsg"):
					prev = "[链接/卡片]"
				default:
					if len(prev) > 100 {
						prev = prev[:100]
					}
				}
			}
			if len(prev) > 150 {
				prev = prev[:150]
			}
			fresh = append(fresh, liveMsg{
				Table: t, From: from, Preview: prev,
				Time: raw.CreateAt, LocalID: raw.LocalID,
			})
		}
	}

	// 按时间倒序（跨表收集时顺序是乱的）
	sort.SliceStable(fresh, func(a, b int) bool { return fresh[a].Time > fresh[b].Time })

	m.mu.Lock()
	defer m.mu.Unlock()
	if !primed {
		// 首次采样：只留最新的 8 条做参考（fresh 已按时间倒序，取头部）
		if len(fresh) > 8 {
			fresh = fresh[:8]
		}
		m.events = append(fresh, m.events...)
		if len(m.events) > liveKeep {
			m.events = m.events[:liveKeep]
		}
		m.pushed += int64(len(fresh))
	} else if len(fresh) > 0 {
		// 之后每轮的都是新消息，直接推
		m.events = append(fresh, m.events...)
		if len(m.events) > liveKeep {
			m.events = m.events[:liveKeep]
		}
		m.pushed += int64(len(fresh))
	}
	m.primed = true
}

// Snapshot 取最近的实时消息
func (m *LiveMonitor) Snapshot() []liveMsg {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]liveMsg, len(m.events))
	copy(out, m.events)
	return out
}

// LiveSnapshot 取某个账号的实时消息；账号不存在返回空
func LiveSnapshot(wxid string) []liveMsg {
	liveMonMu.Lock()
	m := liveMons[wxid]
	liveMonMu.Unlock()
	if m == nil {
		return nil
	}
	return m.Snapshot()
}




