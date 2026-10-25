package main

import (
	"testing"
	"time"
)

func TestBuildXPayParams_Alipay(t *testing.T) {
	m := &ChatMsg{
		To:     "2088123456789012",
		Amount: 0.10,
		Time:   uint64(time.Date(2026, 10, 17, 19, 2, 31, 0, time.Local).Unix()),
	}
	acc := Account{
		Type: "alipay", Wxid: "2088123456789012",
		ChannelID: "5", ChannelCode: "alipay_app", Enabled: true,
	}
	const secret = "66BIR6wh6H6895InH5UmNam9aW8unV6N"
	now := time.Unix(1792913000, 0)

	p := buildXPayParams(m, acc, secret, now)

	want := map[string]string{
		"amount":             "10",
		"pay_type":           "alipay",
		"channel_code":       "alipay_app",
		"channel_account_id": "5",
		"coll_user":          "2088123456789012",
		"pay_time":           "2026-10-17 19:02:31",
		"timestamp":          "1792913000",
	}
	for k, v := range want {
		if p[k] != v {
			t.Errorf("%s = %q，期望 %q", k, p[k], v)
		}
	}

	// 签名必须与"去掉 sign 后重新计算"的结果一致
	unsigned := map[string]string{}
	for k, v := range p {
		if k != "sign" {
			unsigned[k] = v
		}
	}
	if got, exp := p["sign"], xpaySign(unsigned, secret); got != exp {
		t.Errorf("sign = %s，期望 %s", got, exp)
	}
	if len(p["sign"]) != 32 {
		t.Errorf("sign 长度 = %d，期望 32", len(p["sign"]))
	}
}

func TestBuildXPayParams_Wechat(t *testing.T) {
	m := &ChatMsg{
		To:     "wxid_7fr1od8eqymv22",
		Amount: 1.00,
		Time:   uint64(time.Date(2026, 10, 25, 13, 3, 42, 0, time.Local).Unix()),
	}
	acc := Account{
		Type: "wechat", Wxid: "wxid_7fr1od8eqymv22",
		ChannelID: "13", ChannelCode: "jk_wechat_pc", Enabled: true,
	}
	p := buildXPayParams(m, acc, "secret", time.Unix(1792913000, 0))

	if p["pay_type"] != "wxpay" {
		t.Errorf("pay_type = %q，期望 wxpay", p["pay_type"])
	}
	if p["channel_code"] != "jk_wechat_pc" {
		t.Errorf("channel_code = %q", p["channel_code"])
	}
	if p["amount"] != "100" {
		t.Errorf("amount = %q，期望 100（1.00 元）", p["amount"])
	}
}

func TestBuildXPayParams_AmountRounding(t *testing.T) {
	cases := []struct {
		yuan float64
		want string
	}{
		{0.01, "1"},
		{0.10, "10"},
		{1.00, "100"},
		{0.29, "29"},   // 浮点误差边界
		{99.99, "9999"},
		{1234.56, "123456"},
	}
	acc := Account{ChannelID: "1", ChannelCode: "alipay_app"}
	for _, c := range cases {
		m := &ChatMsg{To: "x", Amount: c.yuan, Time: uint64(time.Now().Unix())}
		p := buildXPayParams(m, acc, "s", time.Now())
		if p["amount"] != c.want {
			t.Errorf("%.2f 元 -> %q，期望 %q", c.yuan, p["amount"], c.want)
		}
	}
}

func TestXpayPayType(t *testing.T) {
	cases := []struct {
		code string
		want string
	}{
		{"alipay_app", "alipay"},
		{"jk_alipay", "alipay"},
		{"jk_wechat_pc", "wxpay"},
		{"jk_wechat_app", "wxpay"},
		{"", "wxpay"},
	}
	for _, c := range cases {
		acc := Account{ChannelCode: c.code}
		if got := xpayPayType(acc); got != c.want {
			t.Errorf("channel_code=%q -> %q，期望 %q", c.code, got, c.want)
		}
	}
}

func TestInferChannelCode(t *testing.T) {
	cases := []struct {
		id   string
		want string
	}{
		{"wxid_7fr1od8eqymv22", "jk_wechat_pc"},
		{"wxid_5518695187012", "jk_wechat_pc"},
		{"2088123456789012", "alipay_app"},
		{"2088", "alipay_app"},
		{"unknown", "jk_wechat_pc"},
	}
	for _, c := range cases {
		if got := inferChannelCode(c.id); got != c.want {
			t.Errorf("%q -> %q，期望 %q", c.id, got, c.want)
		}
	}
}
