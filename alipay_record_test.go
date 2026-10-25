package main

// 收款记录里必须能看到支付宝的记录（此前只有微信，支付宝只在后台轮询里上报）。

import (
	"testing"
	"time"
)

func TestAlipayRecordShowsUpInRecords(t *testing.T) {
	e := &Engine{}
	now := time.Now()
	m := &ChatMsg{
		From: "支付宝", To: "2088123456789012", Amount: 0.01,
		Time: uint64(now.Unix()), MsgID: hashToUint64("2026102523001461781454714313"),
		Content: "收钱码收款", RawText: "收钱码收款\n交易号 2026102523001461781454714313",
	}
	e.addHistoryEx(m, false, "", nil, "支付宝", alipayChannelCode(m.To))

	items := e.historyCopy(10)
	if len(items) != 1 {
		t.Fatalf("应有 1 条记录，实际 %d", len(items))
	}
	v := items[0]
	if v.Platform != "支付宝" {
		t.Fatalf("平台应为 支付宝，实际 %q", v.Platform)
	}
	if v.ChannelCode != "alipay_app" {
		t.Fatalf("通道编码应为 alipay_app，实际 %q", v.ChannelCode)
	}
	if v.To != "2088123456789012" {
		t.Fatalf("收款账号应为支付宝 PID，实际 %q", v.To)
	}
	if v.Amount != 0.01 {
		t.Fatalf("金额应为 0.01，实际 %v", v.Amount)
	}
	if v.OrderStatus != "已收款" {
		t.Fatalf("订单状态应为 已收款，实际 %q", v.OrderStatus)
	}
	// 没配置接口 / 未发请求时是「未上报」，不能算回调失败
	if v.CallbackStatus != "未上报" {
		t.Fatalf("回调状态应为 未上报，实际 %q", v.CallbackStatus)
	}
}

func TestAlipayRecordMarksCallbackFailure(t *testing.T) {
	e := &Engine{}
	res := &ReportResult{
		Delivered: true, OK: false, Code: 400,
		Body: `{"code":400,"message":"通道 ID、插件或支付方式不匹配"}`,
		Err:  `平台返回 code=400 message=通道 ID、插件或支付方式不匹配`,
		URL:  "https://epay.example/api/report/1000/pc",
	}
	m := &ChatMsg{
		From: "支付宝", To: "2088123456789012", Amount: 0.02,
		Time: uint64(time.Now().Unix()), MsgID: 1, Content: "收钱码收款",
	}
	e.addHistoryEx(m, false, res.Err, res, "支付宝", "alipay_app")

	v := e.historyCopy(1)[0]
	if v.CallbackStatus != "回调失败" {
		t.Fatalf("回调状态应为 回调失败，实际 %q", v.CallbackStatus)
	}
	if v.CallbackURL == "" || v.CallbackBody == "" {
		t.Fatalf("失败记录要留下请求地址和响应：url=%q body=%q", v.CallbackURL, v.CallbackBody)
	}
	if v.Err == "" {
		t.Fatal("失败记录要有原因")
	}
}

// 微信记录仍然是原来的平台/通道，不能被改动
func TestWechatRecordKeepsDefaults(t *testing.T) {
	e := &Engine{}
	m := &ChatMsg{
		From: "wxid_abc", To: "wxid_7fr1od8eqymv22", Amount: 0.10,
		Time: uint64(time.Now().Unix()), MsgID: 2, Content: "微信支付收款 0.10元",
	}
	e.addHistory(m, false, "未命中关键词", nil)

	v := e.historyCopy(1)[0]
	if v.Platform != "微信" || v.ChannelCode != "jk_wechat_pc" {
		t.Fatalf("微信记录默认值被改了: platform=%q channel=%q", v.Platform, v.ChannelCode)
	}
}

// 登录态失效的支付宝账号不能再报「在线」，否则平台会派单给收不到通知的通道
func TestAlipaySessionOnline(t *testing.T) {
	aliMu.Lock()
	old := aliList
	aliList = []AlipaySession{
		{ID: "a", PID: "2088000000000001", Status: "confirmed",
			Cookies: []AlipayCookie{{Name: "ctoken", Value: "x"}}},
		{ID: "b", PID: "2088000000000002", Status: "expired",
			Cookies: []AlipayCookie{{Name: "ctoken", Value: "x"}}},
		{ID: "c", PID: "2088000000000003", Status: "confirmed"}, // 没 Cookie 也拉不到账单
	}
	aliMu.Unlock()
	defer func() {
		aliMu.Lock()
		aliList = old
		aliMu.Unlock()
	}()

	if !alipaySessionOnline("2088000000000001") {
		t.Fatal("已确认且有 Cookie 的账号应算在线")
	}
	if alipaySessionOnline("2088000000000002") {
		t.Fatal("登录失效的账号不该算在线")
	}
	if alipaySessionOnline("2088000000000003") {
		t.Fatal("没有 Cookie 的账号不该算在线")
	}
	if alipaySessionOnline("2088000000000009") {
		t.Fatal("没有会话的账号不该算在线")
	}
	if alipaySessionOnline("") {
		t.Fatal("空 PID 不该算在线")
	}
}
