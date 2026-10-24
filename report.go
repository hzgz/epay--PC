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
}

// buildInner 组装内层 content JSON（与安卓一致，PC 端改 device/package）
func buildInner(m *ChatMsg) string {
	title := m.Display
	if title == "" {
		title = m.Content
	}
	obj := map[string]string{
		"title":        title,
		"msg":          m.Content,
		"package_name": "com.tencent.wechat.pc",
		"receive_time": microTime(m.Time),
		"device_name":  "Windows PC",
	}
	b, _ := json.Marshal(obj)
	return string(b)
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
	inner := buildInner(m)
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
	txt := strings.TrimSpace(string(rb))
	ok := resp.StatusCode >= 200 && resp.StatusCode < 300
	return ReportResult{OK: ok, Code: resp.StatusCode, Body: txt, URL: u, Sent: string(body)}
}

// ---------- 码支付 V1 ----------

func sendV1(m *ChatMsg, c Config) ReportResult {
	site := strings.TrimRight(strings.TrimSpace(c.V1Site), "/")
	if site == "" || c.V1ChannelID == "" || c.V1Secret == "" {
		return ReportResult{Err: "未配置码支付域名/通道/密钥"}
	}
	inner := buildInner(m)
	ts := fmt.Sprintf("%d", time.Now().UnixMilli())

	mac := hmac.New(sha256.New, []byte(c.V1Secret))
	mac.Write([]byte(ts + "\n" + c.V1Secret))
	sign := base64.StdEncoding.EncodeToString(mac.Sum(nil))

	form := url.Values{}
	form.Set("from", "pc")
	form.Set("content", inner)
	form.Set("timestamp", ts)
	form.Set("sign", sign)
	body := form.Encode()

	u := fmt.Sprintf("%s/api/pay/%s/notify", site, url.PathEscape(c.V1ChannelID))
	req, err := http.NewRequest("POST", u, strings.NewReader(body))
	if err != nil {
		return ReportResult{Err: err.Error(), URL: u}
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", "okhttp/4.12.0")
	client := &http.Client{Timeout: 8 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return ReportResult{Err: err.Error(), URL: u, Sent: body}
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(resp.Body)
	txt := strings.TrimSpace(string(rb))
	ok := resp.StatusCode >= 200 && resp.StatusCode < 300 && txt == "200"
	return ReportResult{OK: ok, Code: resp.StatusCode, Body: txt, URL: u, Sent: body}
}

func Report(m *ChatMsg) ReportResult {
	c := GetConfig()
	switch c.Mode {
	case "yjk":
		return sendYjk(m, c)
	case "v1":
		return sendV1(m, c)
	default:
		return ReportResult{OK: true, Body: "(仅打印，未上报)"}
	}
}
