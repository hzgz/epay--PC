package main

// 上报：易简控 YiJianKong / 码支付V1

import (
	"crypto/hmac"
	"crypto/md5"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

type ReportResult struct {
	OK     bool
	Code   int
	Body   string
	Err    string
	Sent   string
	URL    string

	// Delivered 表示是否真的向平台发出了 HTTP 请求。
	// 仅记录模式下为 false，用于区分「未上报」和「已回调」。
	Delivered bool
}

// mpay 的 PC 模式只认这两个公众号，其余号会被 source_mismatch 拒掉。
// 名单外（例如微信收款商业版换了 gh_id）自动降级成手机模式：
// from 用 com.tencent.mm，content 只放 title + msg，平台按手机合同解析。
var pcAccounts = map[string]string{
	"gh_3dfda90e39d6": "微信支付",
	"gh_f0a92aa7146c": "微信收款助手",
}

// 手机模式下平台接受的 title（写死在 WechatReceiptPayment::receipt）
var mobileTitles = map[string]bool{
	"微信支付":     true,
	"微信收款助手":   true,
	"微信收款商业版": true,
}

// buildReport 返回 (from, content)。ok=false 表示这条消息没有平台能接受的格式。
func buildReport(m *ChatMsg, channelID string) (from, content string, ok bool) {
	title := strings.TrimSpace(m.Display)
	if title == "" {
		title = strings.TrimSpace(m.Content)
	}

	// 路径一：名单内的公众号 → PC 合同（完整 XML，字段最可信）
	if name, hit := pcAccounts[m.GHFrom]; hit && strings.HasPrefix(strings.TrimSpace(m.RawText), "<") {
		acc := m.GHFrom
		_ = name
		obj := map[string]any{
			"event":   "wxtool.officialAccount.message",
			"account": map[string]any{"username": acc},
			"message": map[string]any{
				"username":       acc,
				"source":         "message",
				"isFallback":     false,
				"senderUsername": acc,
				"direction":      "incoming",
				"localType":      21474836529,
				"createTime":     int64(m.Time),
				"sortSeq":        m.SortSeq,
				"localId":        m.LocalID,
				"content":        m.RawText,
			},
			"delivery": map[string]any{
				"id":         fmt.Sprintf("delivery_%d_%d", m.LocalID, m.Time),
				"messageKey": fmt.Sprintf("%s:%s:%d:%d:%d", channelID, acc, m.SortSeq, m.Time, m.LocalID),
			},
		}
		b, _ := json.Marshal(obj)
		return acc, string(b), true
	}

	// 路径二：名单外的公众号 → 手机合同兜底
	// title 必须是平台认可的三个之一，否则发了也会被 unsupported_message 拒。
	if m.GHFrom != "" && m.Amount > 0 {
		name := strings.TrimSpace(m.GHName)
		if !mobileTitles[name] {
			// 公众号显示名不在白名单里，用金额和正文拼一条能过正则的 msg
			name = "微信收款商业版"
		}
		msg := title
		if m.Amount > 0 {
			msg = fmt.Sprintf("%s 收款到账¥%.2f", name, m.Amount)
		}
		obj := map[string]string{
			"title": name,
			"msg":   msg,
		}
		b, _ := json.Marshal(obj)
		return "com.tencent.mm", string(b), true
	}

	// 路径三：聊天消息（hook 路径），保留原格式
	obj := map[string]string{
		"title":        title,
		"msg":          m.Content,
		"package_name": "com.tencent.wechat.pc",
		"receive_time": microTime(m.Time),
		"device_name":  "Windows PC",
	}
	b, _ := json.Marshal(obj)
	return "com.tencent.mm", string(b), true
}

// buildInner 兼容旧调用点
func buildInner(m *ChatMsg, channelID string) string {
	_, c, _ := buildReport(m, channelID)
	return c
}

func microTime(ts uint64) string {
	var t time.Time
	if ts > 1e12 {
		t = time.UnixMilli(int64(ts))
	} else if ts > 1e9 {
		t = time.Unix(int64(ts), 0)
	} else {
		t = time.Now()
	}
	return t.Format("2006-01-02 15:04:05") + fmt.Sprintf(".%06d", t.Nanosecond()/1000)
}

// ---------- 易简控 ----------

func calcYjkSign(fields map[string]string, key string) string {
	if key == "" {
		return ""
	}
	names := make([]string, 0, len(fields))
	for k := range fields {
		if k == "sign" || k == "sign_type" {
			continue
		}
		names = append(names, k)
	}
	sort.Strings(names)
	var sb strings.Builder
	for _, n := range names {
		v := fields[n]
		if strings.TrimSpace(v) == "" {
			continue
		}
		if sb.Len() > 0 {
			sb.WriteByte('&')
		}
		sb.WriteString(n)
		sb.WriteByte('=')
		sb.WriteString(v)
	}
	sb.WriteString(key)
	sum := md5.Sum([]byte(sb.String()))
	return hex.EncodeToString(sum[:])
}

func sendYjk(m *ChatMsg, c Config) ReportResult {
	site := strings.TrimRight(strings.TrimSpace(c.YjkSite), "/")
	if site == "" || c.YjkPid == "" {
		return ReportResult{Err: "未配置易简控域名或 PID"}
	}
	inner := buildInner(m, c.YjkPid)
	fields := map[string]string{
		"from":      "pc",
		"content":   inner,
		"timestamp": fmt.Sprintf("%d", time.Now().UnixMilli()),
	}
	fields["sign"] = calcYjkSign(fields, c.YjkToken)

	body, _ := json.Marshal(fields)
	u := fmt.Sprintf("%s/api/report/%s?token=%s", site,
		url.QueryEscape(c.YjkPid), url.QueryEscape(c.YjkToken))

	req, err := http.NewRequest("POST", u, strings.NewReader(string(body)))
	if err != nil {
		return ReportResult{Err: err.Error(), URL: u}
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("User-Agent", "YiJianKong")
	client := &http.Client{Timeout: 8 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return ReportResult{Err: err.Error(), URL: u, Sent: string(body)}
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(resp.Body)
	logDebug("callback", "V1 响应 <- %d  %s", resp.StatusCode, truncate(string(rb), 200))
	txt := strings.TrimSpace(string(rb))
	ok := resp.StatusCode >= 200 && resp.StatusCode < 300
	return ReportResult{OK: ok, Code: resp.StatusCode, Body: txt, URL: u, Sent: string(body), Delivered: true}
}

// ---------- 码支付 V1 ----------

func sendV1(m *ChatMsg, c Config) ReportResult {
	site := strings.TrimRight(strings.TrimSpace(c.V1Site), "/")
	if site == "" || c.V1Secret == "" {
		return ReportResult{Err: "未配置码支付域名/密钥"}
	}

	// 通道 ID 来自账号绑定（收款账号 = m.To），不走全局配置。
	// 码支付路径固定为 /api/pay/{通道}/notify，PID 是商户 ID，不参与拼接。
	acc, ok := findAccount(m.To)
	channel := ""
	if ok {
		channel = strings.TrimSpace(acc.ChannelID)
	}
	if channel == "" {
		return ReportResult{Err: fmt.Sprintf("收款账号 %s 未绑定通道", short(m.To))}
	}
	sender, inner, _ := buildReport(m, channel)
	ts := fmt.Sprintf("%d", time.Now().UnixMilli())

	mac := hmac.New(sha256.New, []byte(c.V1Secret))
	mac.Write([]byte(ts + "\n" + c.V1Secret))
	sign := base64.StdEncoding.EncodeToString(mac.Sum(nil))

	form := url.Values{}
	form.Set("from", sender)
	form.Set("content", inner)
	form.Set("timestamp", ts)
	form.Set("sign", sign)
	body := form.Encode()

	u := fmt.Sprintf("%s/api/pay/%s/notify", site, url.PathEscape(channel))
	logDebug("callback", "V1 上报 -> %s", u)
	logDebug("callback", "V1 body: %s", truncate(body, 300))
	req, err := http.NewRequest("POST", u, strings.NewReader(body))
	if err != nil {
		return ReportResult{Err: err.Error(), URL: u}
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", "okhttp/4.12.0")
	client := &http.Client{Timeout: 8 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		logError("callback", "V1 请求失败: %v", err)
		return ReportResult{Err: err.Error(), URL: u, Sent: body}
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(resp.Body)
	logDebug("callback", "V1 响应 <- %d  %s", resp.StatusCode, truncate(string(rb), 200))
	txt := strings.TrimSpace(string(rb))
	succ := resp.StatusCode >= 200 && resp.StatusCode < 300 && txt == "200"
	return ReportResult{OK: succ, Code: resp.StatusCode, Body: txt, URL: u, Sent: body, Delivered: true}
}

func Report(m *ChatMsg) ReportResult {
	c := GetConfig()
	switch c.Mode {
	case "yjk":
		return sendYjk(m, c)
	case "v1":
		return sendV1(m, c)
	default:
		// 未配置接口：只记录，不发请求
		return ReportResult{OK: true, Body: "(仅打印，未上报)", Delivered: false}
	}
}

// firstN 取前 n 个字符，用于日志里显示密钥/签名片段
func firstN(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
