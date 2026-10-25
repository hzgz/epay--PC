package main

import "testing"

func TestAlipayLooksLikeLoginPage(t *testing.T) {
	cases := []struct {
		name string
		body string
		want bool
	}{
		{
			"真实登录页特征",
			`<html><head><title>登录 - 支付宝</title></head>
			<body><form id="loginForm"><input type="password" name="password"></form></body></html>`,
			true,
		},
		{
			"仅 password 输入框",
			`<html><body><input type="password" id="pwd"></body></html>`,
			true,
		},
		{
			"title 含 login",
			`<html><head><title>alipay login</title></head><body>hi</body></html>`,
			true,
		},
		{
			"正常账单页",
			`<html><head><title>账目明细</title></head>
			<body><table><tr><td>2026-10-17 19:02:31</td><td>Kudi-转账</td>
			<td>0.10</td><td>20261017200040011100780049961241</td></tr></table></body></html>`,
			false,
		},
		{
			"空",
			``,
			false,
		},
	}
	for _, c := range cases {
		if got := alipayLooksLikeLoginPage([]byte(c.body)); got != c.want {
			t.Errorf("[%s] = %v，期望 %v", c.name, got, c.want)
		}
	}
}

func TestAlipayLooksLikeErrorPage(t *testing.T) {
	cases := []struct {
		name string
		body string
		want bool
	}{
		{
			"服务器繁忙",
			`<html><head><title>服务器繁忙</title>
			<link rel="stylesheet" href="https://gw.alipayobjects.com/os/s/prod/alipay_site/wait-7ab31.css"></head></html>`,
			true,
		},
		{
			"只有 wait css",
			`<html><head><link href="/os/s/prod/alipay_site/wait-abc.css"></head></html>`,
			true,
		},
		{
			"请稍后重试",
			`<html><body>系统繁忙，请稍后重试</body></html>`,
			true,
		},
		{
			"正常账单页",
			`<html><head><title>账目明细</title></head>
			<body><table><tr><td>0.10</td><td>20261017200040011100780049961241</td></tr></table></body></html>`,
			false,
		},
		{
			"空响应算失败",
			``,
			true,
		},
	}
	for _, c := range cases {
		if got := alipayLooksLikeErrorPage([]byte(c.body)); got != c.want {
			t.Errorf("[%s] = %v，期望 %v", c.name, got, c.want)
		}
	}
}

func TestAlipayDetect_NoFalsePositiveOnRealBill(t *testing.T) {
	// 一份带交易号、金额、中文说明的正常账单，两个检测都不该命中
	bill := `<html><head><title>账目明细</title></head><body>
	<div class="record-item">
	  <span class="time">2026-10-17 19:02:31</span>
	  <span class="memo">收钱码收款</span>
	  <span class="amount">0.10</span>
	  <span class="trade-no">20261017200040011100780049961241</span>
	</div></body></html>`
	if alipayLooksLikeLoginPage([]byte(bill)) {
		t.Error("正常账单被误判为登录页")
	}
	if alipayLooksLikeErrorPage([]byte(bill)) {
		t.Error("正常账单被误判为错误页")
	}
	got := parseAlipayBillHTML(bill)
	if len(got) != 1 {
		t.Fatalf("账单应解析出 1 条，实际 %d", len(got))
	}
	if got[0].TradeNo != "20261017200040011100780049961241" {
		t.Errorf("交易号 = %s", got[0].TradeNo)
	}
}
