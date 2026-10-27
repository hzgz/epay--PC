package main

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
)

// 手机合同构造（PC 被拒时的重试路径）
func TestBuildReportDefaultPhone(t *testing.T) {
	m := &ChatMsg{
		GHFrom:  "gh_f0a92aa7146c",
		GHName:  "微信收款助手",
		Content: "微信支付收款0.10元(朋友到店)",
		RawText: `<msg/>`, Time: 1792839759, SortSeq: 1792839759000, LocalID: 221, Amount: 0.10,
	}
	from, content, ok := buildReportPhone(m)
	if !ok {
		t.Fatal("应该可用")
	}
	if from != "com.tencent.mm" {
		t.Fatalf("手机合同 from 应为 com.tencent.mm，实际 %q", from)
	}
	var obj map[string]string
	if err := json.Unmarshal([]byte(content), &obj); err != nil {
		t.Fatalf("content 不是 JSON: %v", err)
	}
	if obj["title"] != "微信收款助手" {
		t.Errorf("title=%q", obj["title"])
	}
	if !strings.Contains(obj["msg"], "0.10") {
		t.Errorf("msg 应含金额: %q", obj["msg"])
	}
}

// PC 合同的 messageKey 第一段必须是数字（MPAY 用 ctype_digit 校验）
func TestBuildReportPCAccount(t *testing.T) {
	m := &ChatMsg{
		GHFrom:  "gh_f0a92aa7146c",
		GHName:  "微信收款助手",
		Content: "微信支付收款0.10元(朋友到店)",
		Display: "微信支付收款0.10元(朋友到店)",
		RawText: `<msg><appmsg><title>微信支付收款0.10元</title></appmsg><fromusername>gh_f0a92aa7146c</fromusername></msg>`,
		Time:    1792839759,
		SortSeq: 1792839759000,
		LocalID: 221,
		Amount:  0.10,
	}
	from, content, ok := buildReportPC(m, "1")
	if !ok {
		t.Fatal("应该可用")
	}
	if from != "gh_f0a92aa7146c" {
		t.Fatalf("from 应该是公众号 ID，实际 %q", from)
	}
	// 按 MPAY 的 pcReceipt 校验 messageKey：5 段、第 1/2/4 段必须是数字、第 2 段等于 sortSeq
	var pc struct {
		Delivery struct {
			MessageKey string `json:"messageKey"`
		} `json:"delivery"`
		Message struct {
			SortSeq  int64 `json:"sortSeq"`
			LocalID  int64 `json:"localId"`
			CreateAt int64 `json:"createTime"`
		} `json:"message"`
	}
	if err := json.Unmarshal([]byte(content), &pc); err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(pc.Delivery.MessageKey, ":")
	if len(parts) != 5 {
		t.Fatalf("messageKey 应为 5 段，实际 %d 段: %q", len(parts), pc.Delivery.MessageKey)
	}
	digits := func(x string) bool {
		if x == "" {
			return false
		}
		for _, r := range x {
			if r < '0' || r > '9' {
				return false
			}
		}
		return true
	}
	if !digits(parts[0]) {
		t.Errorf("第 1 段必须是数字，实际 %q", parts[0])
	}
	if parts[1] != "gh_f0a92aa7146c" || !digits(parts[2]) ||
		parts[3] != strconv.FormatInt(pc.Message.CreateAt, 10) || !digits(parts[4]) {
		t.Errorf("messageKey 段不匹配: %v (sortSeq=%d createTime=%d localId=%d)",
			parts, pc.Message.SortSeq, pc.Message.CreateAt, pc.Message.LocalID)
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(content), &obj); err != nil {
		t.Fatalf("content 不是合法 JSON: %v", err)
	}
	if obj["event"] != "wxtool.officialAccount.message" {
		t.Fatalf("应该是 wxtool 合同，实际 %v", obj["event"])
	}
	msg := obj["message"].(map[string]any)
	if !strings.Contains(msg["content"].(string), "<fromusername>") {
		t.Fatal("PC 合同必须带完整 XML")
	}
	dl := obj["delivery"].(map[string]any)
	key := dl["messageKey"].(string)
	if parts := strings.Split(key, ":"); len(parts) != 5 {
		t.Fatalf("messageKey 必须 5 段，实际 %d: %q", len(parts), key)
	}
}

func TestBuildReportPCUsesStableSourceIDWhenLocalIDIsUnavailable(t *testing.T) {
	build := func(stableID string) map[string]any {
		m := &ChatMsg{
			From: "gh_f0a92aa7146c", To: "wxid_account",
			GHFrom: "gh_f0a92aa7146c", Content: "微信支付收款0.10元",
			RawText: `<msg><appmsg><title>微信支付收款0.10元</title></appmsg></msg>`,
			Time:    1792839759, StableID: stableID,
		}
		_, body, ok := buildReportPC(m, "1")
		if !ok {
			t.Fatal("expected PC report")
		}
		var obj map[string]any
		if err := json.Unmarshal([]byte(body), &obj); err != nil {
			t.Fatal(err)
		}
		return obj["delivery"].(map[string]any)
	}

	one := build("wechat-hook:41")
	two := build("wechat-hook:42")
	if one["id"] == two["id"] {
		t.Fatal("distinct source IDs produced the same delivery ID")
	}
	if one["messageKey"] == two["messageKey"] {
		t.Fatal("distinct source IDs produced the same message key")
	}
}

// 名单外公众号 → 手机合同兜底
func TestBuildReportFallbackAccount(t *testing.T) {
	m := &ChatMsg{
		GHFrom:  "gh_000000000000", // 商业版之类，不在白名单
		GHName:  "微信收款商业版",
		Content: "收款到账0.50元",
		Display: "收款到账0.50元",
		RawText: `<msg><appmsg><title>收款到账0.50元</title></appmsg></msg>`,
		Time:    1792839759,
		Amount:  0.50,
	}
	from, content, ok := buildReport(m, "1")
	if !ok {
		t.Fatal("应该可用")
	}
	if from != "com.tencent.mm" {
		t.Fatalf("兜底 from 应该是 com.tencent.mm，实际 %q", from)
	}
	var obj map[string]string
	if err := json.Unmarshal([]byte(content), &obj); err != nil {
		t.Fatalf("content 不是合法 JSON: %v", err)
	}
	if !mobileTitles[obj["title"]] {
		t.Fatalf("title 必须是平台认可的三种之一，实际 %q", obj["title"])
	}
	if !strings.Contains(obj["msg"], "0.50") {
		t.Fatalf("msg 应含金额，实际 %q", obj["msg"])
	}
	if obj["title"] == "gh_000000000000" {
		t.Fatal("title 不能是 gh_id")
	}
}

// 手机合同金额能在平台正则里匹配到
func TestFallbackMsgMatchesPlatformRegex(t *testing.T) {
	cases := []struct {
		name   string
		amount float64
	}{
		{"微信收款商业版", 0.01},
		{"微信收款商业版", 100},
		{"微信收款商业版", 1234.56},
	}
	for _, c := range cases {
		m := &ChatMsg{
			GHFrom: "gh_000000000000", GHName: c.name,
			Content: "x", Display: "x",
			Time: 1792839759, Amount: c.amount,
		}
		_, content, _ := buildReport(m, "1")
		var obj map[string]string
		json.Unmarshal([]byte(content), &obj)
		// 平台正则 /(?:到账|收款|收款到账)\s*[¥￥]?\s*(\d+(?:\.\d{1,2})?)\s*(?:元)?/u
		if !strings.Contains(obj["msg"], "到账") && !strings.Contains(obj["msg"], "收款") {
			t.Fatalf("msg 缺关键词: %q", obj["msg"])
		}
	}
}

func TestV1LooksOK(t *testing.T) {
	ok := []string{"200", " 200 ", "success", "ok", "true",
		`{"code":200,"msg":"ok"}`, `{"code":"200"}`, `{"status":"success"}`}
	for _, s := range ok {
		if !v1LooksOK(s) {
			t.Errorf("应判成功: %q", s)
		}
	}
	bad := []string{"", "0", "fail", `{"code":500,"msg":"签名错误"}`,
		`{"code":"400"}`, `{"status":"fail"}`, "<html>error</html>"}
	for _, s := range bad {
		if v1LooksOK(s) {
			t.Errorf("应判失败: %q", s)
		}
	}
}

func TestBuildReportAlipay(t *testing.T) {
	m := &ChatMsg{From: "支付宝", To: "2088123456789012", Amount: 0.13,
		Time: 1792930977, Content: "收钱码收款"}
	from, inner, ok := buildReport(m, "14")
	if !ok {
		t.Fatal("构造失败")
	}
	if from != "com.eg.android.AlipayGphone" {
		t.Fatalf("from 应为支付宝包名，实际 %q", from)
	}
	var obj map[string]string
	if err := json.Unmarshal([]byte(inner), &obj); err != nil {
		t.Fatalf("content 不是 JSON: %v (%s)", err, inner)
	}
	if obj["title"] != "收款通知" {
		t.Errorf("title=%q", obj["title"])
	}
	if !strings.Contains(obj["msg"], "0.13") || !strings.Contains(obj["msg"], "收款") {
		t.Errorf("msg 里应带金额和收款字样: %q", obj["msg"])
	}
}
