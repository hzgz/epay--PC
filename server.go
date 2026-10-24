package main

// 内嵌 HTTP 服务：提供 UI 与配置/状态 API

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
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
}

type MsgView struct {
	From     string  `json:"from"`
	To       string  `json:"to"`
	Content  string  `json:"content"`
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
	total := e.stats.Reported + e.stats.Failed
	rate := 0.0
	if total > 0 {
		rate = float64(e.stats.Reported) / float64(total) * 100
	}
	return StatusResp{
		Attached:      e.hooks != nil,
		Pid:           e.stats.Pid,
		WechatRunning: PidByName(WeChatProc) != 0,
		Scanned:       e.stats.Scanned,
		Parsed:        e.stats.Parsed,
		Forwarded:     e.stats.Forwarded,
		Reported:      e.stats.Reported,
		Failed:        e.stats.Failed,
		Mode:          c.Mode,
		ForwardAll:    c.ForwardAll,
		TodaySum:      e.stats.TodaySum,
		TodayCount:    e.stats.TodayCount,
		YesterdaySum:  e.stats.YesterdaySum,
		YesterdayCnt:  e.stats.YesterdayCnt,
		WeekSum:       e.stats.WeekSum,
		WeekCount:     e.stats.WeekCount,
		AllSum:        e.stats.AllSum,
		AllCount:      e.stats.AllCount,
		SuccessRate:   rate,
	}
}

// DebugItem 调试用：金额识别之前的完整消息结构
type DebugItem struct {
	Seq      int     `json:"seq"`
	From     string  `json:"from"`
	To       string  `json:"to"`
	Content  string  `json:"content"`
	Display  string  `json:"display"`
	Time     uint64  `json:"time"`
	MsgID    uint64  `json:"msgid"`
	Amount   float64 `json:"amount"`
	Bytes    int     `json:"bytes"`
	RawHead  string  `json:"raw_head"`
	At       string  `json:"at"`
}

const debugMax = 20

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
	if res != nil {
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
	}

	v := MsgView{
		From: m.From, To: m.To, Content: m.Content, Display: m.Display,
		Time: m.Time, RawID: fmt.Sprintf("%d", m.MsgID), Amount: m.Amount,
		Reported: reported, Err: errMsg,
		Platform: "微信", ChannelCode: "jk_wechat_pc", OrderStatus: orderStatus,
		CallbackStatus: cbStatus, CallbackBody: cbBody, CallbackURL: cbURL,
	}
	e.history = append([]MsgView{v}, e.history...)
	if len(e.history) > 500 {
		e.history = e.history[:500]
	}
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
		removeAccount(wxid)
		w.Write([]byte(`{"ok":true}`))
	})

	mux.HandleFunc("/api/sysinfo", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(sysInfo())
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









