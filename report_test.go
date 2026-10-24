package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// 名单内公众号 → PC 合同（完整 XML + gh_ from）
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
	from, content, ok := buildReport(m, "1")
	if !ok {
		t.Fatal("应该可用")
	}
	if from != "gh_f0a92aa7146c" {
		t.Fatalf("from 应该是公众号 ID，实际 %q", from)
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
