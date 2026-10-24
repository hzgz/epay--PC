package main

// 内嵌 HTTP 服务：提供 UI 与配置/状态 API

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"
)

//go:embed ui/index.html
var uiHTML []byte

type StatusResp struct {
	Attached      bool   `json:"attached"`
	Pid           uint32 `json:"pid"`
	WechatRunning bool   `json:"wechat_running"`
	Scanned       int    `json:"scanned"`
	Parsed        int    `json:"parsed"`
	Forwarded     int    `json:"forwarded"`
	Reported      int    `json:"reported"`
	Failed        int    `json:"failed"`
	Mode          string `json:"mode"`
	ForwardAll    bool   `json:"forward_all"`

	TodaySum     float64 `json:"today_sum"`
	TodayCount   int     `json:"today_count"`
	YesterdaySum float64 `json:"yesterday_sum"`
	YesterdayCnt int     `json:"yesterday_count"`
	WeekSum      float64 `json:"week_sum"`
	WeekCount    int     `json:"week_count"`
	AllSum       float64 `json:"all_sum"`
	AllCount     int     `json:"all_count"`
	SuccessRate  float64 `json:"success_rate"`

	// 各采集点独立计数
	Points []PointStat `json:"points"`
}

type PointStat struct {
	Name    string `json:"name"`
	Scanned int    `json:"scanned"`
	Parsed  int    `json:"parsed"`
}

type MsgView struct {
	From     string  `json:"from"`
	To       string  `json:"to"`
	Content  string  `json:"content"`
	RawText  string  `json:"raw_content"` // XML 原文，详情页展示用
	Display  string  `json:"display"`
	Time     uint64  `json:"time"`
	RawID    string  `json:"rawid"`
	Amount   float64 `json:"amount"`
	Reported bool    `json:"reported"`
	Err      string  `json:"err"`

	// 订单记录字段
	Platform       string `json:"platform"`        // 微信
	ChannelCode    string `json:"channel_code"`    // jk_wechat_pc
	OrderStatus    string `json:"order_status"`    // 已收款 / 未识别
	CallbackStatus string `json:"callback_status"` // 已回调 / 回调失败 / 未上报
	CallbackBody   string `json:"callback_body"`   // 回调响应全文
	CallbackURL    string `json:"callback_url"`    // 实际请求地址
}

func (e *Engine) snapshot() StatusResp {
	e.mu.Lock()
	defer e.mu.Unlock()
	c := GetConfig()

	// 统计全部从已持久化的记录重算：重启后首页不为 0，也方便对账。
	now := time.Now()
	todayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).Unix()
	ydayStart := todayStart - 86400
	weekStart := todayStart - 6*86400

	reported, failed, forwarded := 0, 0, 0
	var todaySum, ydaySum, weekSum, allSum float64
	todayCnt, ydayCnt, weekCnt, allCnt := 0, 0, 0, 0
	for _, m := range e.history {
		switch m.CallbackStatus {
		case "已回调":
			reported++
		case "回调失败":
			failed++
		}
		if m.Amount <= 0 {
			continue
		}
		forwarded++
		ts := int64(m.Time)
		if ts > 1e12 { // 毫秒
			ts /= 1000
		}
		allSum += m.Amount
		allCnt++
		if ts >= weekStart {
			weekSum += m.Amount
			weekCnt++
		}
		if ts >= todayStart {
			todaySum += m.Amount
			todayCnt++
		} else if ts >= ydayStart {
			ydaySum += m.Amount
			ydayCnt++
		}
	}
	rate := 0.0
	if total := reported + failed; total > 0 {
		rate = float64(reported) / float64(total) * 100
	}

	// 监听是否在跑：hook 或数据库监控任一生效即算在线
	dbSt := WxStatus()
	dbLive := dbSt.Running
	live := e.hooks != nil || dbLive
	pid := e.stats.Pid
	if pid == 0 && dbLive {
		pid = PidByName(WeChatProc)
	}

	return StatusResp{
		Attached:      live,
		Pid:           pid,
		WechatRunning: PidByName(WeChatProc) != 0,
		Scanned:       dbSt.PollRounds + e.stats.Scanned,
		Parsed:        forwarded,
		Forwarded:     forwarded,
		Reported:      reported,
		Failed:        failed,
		Mode:          c.Mode,
		ForwardAll:    c.ForwardAll,
		TodaySum:      todaySum,
		TodayCount:    todayCnt,
		YesterdaySum:  ydaySum,
		YesterdayCnt:  ydayCnt,
		WeekSum:       weekSum,
		WeekCount:     weekCnt,
		AllSum:        allSum,
		AllCount:      allCnt,
		SuccessRate:   rate,
		Points:        e.pointStatsLocked(),
	}
}

// DebugItem 调试用：金额识别之前的完整消息结构
type DebugItem struct {
	Seq      int     `json:"seq"`
	From     string  `json:"from"`
	To       string  `json:"to"`
	Content  string  `json:"content"`
	RawText  string  `json:"raw_content"` // XML 原文，详情页展示用
	Display  string  `json:"display"`
	Time     uint64  `json:"time"`
	MsgID    uint64  `json:"msgid"`
	Amount   float64 `json:"amount"`
	Bytes    int     `json:"bytes"`
	RawHead  string  `json:"raw_head"`
	At       string  `json:"at"`
}

const debugMax = 20

// UnkSample 未识别的 pb 结构采样：用来判断某类消息是否经过了 hook
type UnkSample struct {
	Seq   int    `json:"seq"`
	Bytes int    `json:"bytes"`
	At    string `json:"at"`
	From  string `json:"from"`
	To    string `json:"to"`
	Head  string `json:"head"`
	Texts []string `json:"texts"`
}

const unkMax = 40

func (e *Engine) addUnknown(buf []byte) {
	var fs []PBField
	ParsePB(buf, 0, "", &fs)

	texts := make([]string, 0, 10)
	seen := map[string]bool{}
	for _, f := range fs {
		if f.Wire != 2 || len(f.Bytes) < 2 || len(f.Bytes) >= 120 {
			continue
		}
		s := trimStr(f.Bytes)
		if s == "" || seen[s] || len(texts) >= 10 {
			continue
		}
		seen[s] = true
		texts = append(texts, s)
	}

	n := len(buf)
	if n > 48 {
		n = 48
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	e.unkSeq++
	it := UnkSample{
		Seq: e.unkSeq, Bytes: len(buf), At: time.Now().Format("15:04:05"),
		Head: fmt.Sprintf("% X", buf[:n]), Texts: texts,
	}
	e.unknowns = append([]UnkSample{it}, e.unknowns...)
	if len(e.unknowns) > unkMax {
		e.unknowns = e.unknowns[:unkMax]
	}
}

func (e *Engine) addDebug(m *ChatMsg, rawLen int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	head := ""
	if len(m.Raw) > 0 {
		n := len(m.Raw)
		if n > 64 {
			n = 64
		}
		head = fmt.Sprintf("% X", m.Raw[:n])
	}
	e.debugSeq++
	it := DebugItem{
		Seq: e.debugSeq, From: m.From, To: m.To, Content: m.Content,
		Display: m.Display, Time: m.Time, MsgID: m.MsgID, Amount: m.Amount,
		Bytes: rawLen, RawHead: head, At: time.Now().Format("15:04:05"),
	}
	e.debug = append([]DebugItem{it}, e.debug...)
	if len(e.debug) > debugMax {
		e.debug = e.debug[:debugMax]
	}
}

// addHistory 记录一条消息。res 为 nil 表示未上报。
func (e *Engine) addHistory(m *ChatMsg, reported bool, errMsg string, res *ReportResult) {
	e.mu.Lock()
	defer e.mu.Unlock()

	orderStatus := "未识别"
	if m.Amount > 0 {
		orderStatus = "已收款"
	}

	cbStatus := "未上报"
	cbBody := ""
	cbURL := ""
	if res != nil && res.Delivered {
		// 真的向平台发过请求，才谈得上回调成功/失败
		cbURL = res.URL
		cbBody = res.Body
		if reported {
			cbStatus = "已回调"
		} else {
			cbStatus = "回调失败"
			if cbBody == "" {
				cbBody = res.Err
			}
		}
	} else if res != nil {
		// 未配置接口 / 仅记录模式
		cbBody = res.Body
	}

	v := MsgView{
		From: m.From, To: m.To, Content: m.Content, RawText: m.RawText, Display: m.Display,
		Time: m.Time, RawID: fmt.Sprintf("%d", m.MsgID), Amount: m.Amount,
		Reported: reported, Err: errMsg,
		Platform: "微信", ChannelCode: "jk_wechat_pc", OrderStatus: orderStatus,
		CallbackStatus: cbStatus, CallbackBody: cbBody, CallbackURL: cbURL,
	}
	e.history = append([]MsgView{v}, e.history...)
	if len(e.history) > recordsMax {
		e.history = e.history[:recordsMax]
	}
	markRecordsDirty()
}

// pointStatsLocked 调用方必须已持有 e.mu（snapshot 已加锁，不能再 Lock）
func (e *Engine) pointStatsLocked() []PointStat {
	out := make([]PointStat, 0, len(e.hooks))
	for _, hk := range e.hooks {
		out = append(out, PointStat{Name: hk.Point.Name, Scanned: hk.Scanned, Parsed: hk.Parsed})
	}
	return out
}

func (e *Engine) historyCopy(limit int) []MsgView {
	e.mu.Lock()
	defer e.mu.Unlock()
	if limit <= 0 || limit > len(e.history) {
		limit = len(e.history)
	}
	out := make([]MsgView, limit)
	copy(out, e.history[:limit])
	return out
}

func startServer() (int, error) {
	mux := http.NewServeMux()

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Write(uiHTML)
	})

	mux.HandleFunc("/api/status", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(eng.snapshot())
	})

	mux.HandleFunc("/api/messages", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		limit := 200
		if v := q.Get("limit"); v != "" {
			fmt.Sscanf(v, "%d", &limit)
		}
		items := eng.historyCopy(limit)
		// amount_only=1：只给带金额的（收款记录页用，聊天消息不进列表）
		if q.Get("amount_only") == "1" {
			filtered := make([]MsgView, 0, len(items))
			for _, v := range items {
				if v.Amount > 0 {
					filtered = append(filtered, v)
				}
			}
			items = filtered
		}
		json.NewEncoder(w).Encode(items)
	})

	mux.HandleFunc("/api/config", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			var c Config
			if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
				http.Error(w, err.Error(), 400)
				return
			}
			if err := SetConfig(c); err != nil {
				http.Error(w, err.Error(), 500)
				return
			}
			logInfo("config", "配置已更新: mode=%s forward_all=%v", c.Mode, c.ForwardAll)
			w.Write([]byte(`{"ok":true}`))
			return
		}
		json.NewEncoder(w).Encode(GetConfig())
	})

	// 账号列表
	mux.HandleFunc("/api/accounts", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			var a Account
			if err := json.NewDecoder(r.Body).Decode(&a); err != nil || a.Wxid == "" {
				http.Error(w, "bad request", 400)
				return
			}
			upsertAccount(a)
			logInfo("config", "账号通道已保存 wxid=%s 通道=%s", a.Wxid, a.ChannelID)
			w.Write([]byte(`{"ok":true}`))
			return
		}
		json.NewEncoder(w).Encode(accountsCopy())
	})

	mux.HandleFunc("/api/accounts/delete", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			http.Error(w, "POST only", 405)
			return
		}
		wxid := r.URL.Query().Get("wxid")
		if wxid == "" {
			http.Error(w, "wxid required", 400)
			return
		}
		deleteAccountAll(wxid)
		w.Write([]byte(`{"ok":true}`))
	})

	mux.HandleFunc("/api/unknown", func(w http.ResponseWriter, r *http.Request) {
		e := eng
		e.mu.Lock()
		out := make([]UnkSample, len(e.unknowns))
		copy(out, e.unknowns)
		e.mu.Unlock()
		json.NewEncoder(w).Encode(out)
	})

	mux.HandleFunc("/api/sysinfo", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(sysInfo())
	})

	// 扫描本机微信账号目录（只读，不碰微信进程）
	mux.HandleFunc("/api/wx/scan", func(w http.ResponseWriter, r *http.Request) {
		accs := ScanWxAccounts()
		type item struct {
			Wxid    string `json:"wxid"`
			Dir     string `json:"dir"`
			HasBiz  bool   `json:"has_biz"`
			ModTime string `json:"mod_time"`
			Bound   bool   `json:"bound"`
		}
		out := make([]item, 0, len(accs))
		for _, a := range accs {
			_, bound := findAccount(a.Wxid)
			mt := ""
			if !a.ModTime.IsZero() {
				mt = a.ModTime.Format("2006-01-02 15:04")
			}
			out = append(out, item{
				Wxid: a.Wxid, Dir: a.Dir, HasBiz: a.HasBiz,
				ModTime: mt, Bound: bound,
			})
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"root":     WeChatRoot(),
			"accounts": out,
		})
	})

	// 自动获取密钥：hook 微信进程，取数据库密钥
	mux.HandleFunc("/api/wx/getkey", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			http.Error(w, "POST only", 405)
			return
		}
		sec := 12
		if v := r.URL.Query().Get("sec"); v != "" {
			fmt.Sscanf(v, "%d", &sec)
			if sec < 3 {
				sec = 3
			}
			if sec > 60 {
				sec = 60
			}
		}
		res := ExtractKey(time.Duration(sec) * time.Second)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(res)
	})

	// 等待模式：盯微信重启，自动 hook 抓密钥
	mux.HandleFunc("/api/wx/getkey/watch", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			http.Error(w, "POST only", 405)
			return
		}
		sec := 180
		if v := r.URL.Query().Get("sec"); v != "" {
			fmt.Sscanf(v, "%d", &sec)
			if sec < 30 {
				sec = 30
			}
			if sec > 900 {
				sec = 900
			}
		}
		StartKeyWatch(time.Duration(sec) * time.Second)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"ok": true, "sec": sec})
	})

	mux.HandleFunc("/api/wx/getkey/status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(KeyWatchStatusNow())
	})

	// 绑定账号：校验密钥 → 保存账号与配置 → 启动监控
	mux.HandleFunc("/api/wx/bind", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			http.Error(w, "POST only", 405)
			return
		}
		var req struct {
			Wxid      string `json:"wxid"`
			Key       string `json:"key"`
			ChannelID string `json:"channel_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Wxid == "" {
			http.Error(w, "bad request", 400)
			return
		}
		key := strings.TrimSpace(strings.ToLower(req.Key))
		if len(key) != 64 {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{
				"ok": false, "err": "密钥必须是 64 位十六进制",
			})
			return
		}
		acc, found := findWxAccount(req.Wxid)
		if !found {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{
				"ok": false, "err": "未找到该账号的数据目录",
			})
			return
		}
		if !acc.HasBiz {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{
				"ok": false, "err": "该账号没有 biz_message_0.db（未登录过微信 PC 端公众号）",
			})
			return
		}

		// 先试打开，密钥不对就不落盘
		db, err := OpenWxDB(acc.BizDB, key)
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{
				"ok": false, "err": "密钥校验失败：" + err.Error(),
			})
			return
		}
		tables, _ := db.Tables()
		db.Close()

		// 保存账号
		upsertAccount(Account{
			Wxid: req.Wxid, Nick: req.Wxid,
			ChannelID: req.ChannelID, Enabled: true,
		})

		// 保存配置并（重新）启动监控
		cfg := GetConfig()
		cfg.DbEnabled = true
		cfg.DbWxid = req.Wxid
		cfg.DbKey = key
		SetConfig(cfg)

		StopWxMonitor()
		if err := StartWxMonitor(acc.BizDB, key, acc.Wxid, acc); err != nil {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{
				"ok": false, "err": "监控启动失败：" + err.Error(),
			})
			return
		}
		InitDefaultWatches()

		logInfo("config", "账号已绑定 %s（%d 张消息表）", req.Wxid, len(tables))
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"ok": true, "tables": len(tables),
		})
	})

	mux.HandleFunc("/api/logs", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		limit := 300
		if v := q.Get("limit"); v != "" {
			fmt.Sscanf(v, "%d", &limit)
		}
		json.NewEncoder(w).Encode(logsCopy(q.Get("level"), q.Get("module"), limit))
	})

	mux.HandleFunc("/api/logs/clear", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			http.Error(w, "POST only", 405)
			return
		}
		logsClear()
		w.Write([]byte(`{"ok":true}`))
	})

	mux.HandleFunc("/api/debug", func(w http.ResponseWriter, r *http.Request) {
		e := eng
		e.mu.Lock()
		out := make([]DebugItem, len(e.debug))
		copy(out, e.debug)
		e.mu.Unlock()
		json.NewEncoder(w).Encode(out)
	})

	mux.HandleFunc("/api/reinject", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			http.Error(w, "POST only", 405)
			return
		}
		go func() {
			eng.detach()
			Sleep(300)
			if id := PidByName(WeChatProc); id != 0 {
				eng.attach()
			}
		}()
		w.Write([]byte(`{"ok":true}`))
	})

	// 找一个空闲端口
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	port := ln.Addr().(*net.TCPAddr).Port
	go http.Serve(ln, mux)
	return port, nil
}























