package main

import "testing"

func TestExtractAmount(t *testing.T) {
	cases := []struct {
		text string
		want float64
	}{
		// 微信支付收款
		{"微信支付收款0.10元", 0.10},
		{"微信支付收款1.01元", 1.01},
		{"微信支付收款0.10元(朋友到店)", 0.10},
		{"微信支付收款100元", 100},
		{"微信支付收款2.5元", 2.5},
		// 收款助手
		{"微信收款助手：你已成功收款1.01元", 1.01},
		{"你已成功收款0.50元", 0.50},
		// 店员
		{"【店员消息】张三通过扫码向你付款0.50元", 0.50},
		{"店员通：收款成功 3.00 元", 3.00},
		// 二维码赞赏
		{"二维码赞赏到账 5.00元", 5.00},
		// 余额转入
		{"已转入余额 12.34 元", 12.34},
		// 符号形式
		{"收款金额￥0.10", 0.10},
		{"到账 ¥88.88", 88.88},
		// 非收款
		{"在吗", 0},
		{"今天天气不错", 0},
		{"我用的老胡v2", 0},
		{"", 0},
	}
	for _, c := range cases {
		got := extractAmount(c.text)
		if got != c.want {
			t.Errorf("extractAmount(%q) = %v, want %v", c.text, got, c.want)
		}
	}
}

func TestXMLTitle(t *testing.T) {
	cases := []struct {
		xml  string
		want string
	}{
		{`<msg><appmsg><title><![CDATA[微信支付收款0.10元(朋友到店)]]></title><des>x</des></appmsg></msg>`,
			"微信支付收款0.10元(朋友到店)"},
		{`<msg><appmsg><title>微信支付收款1.01元</title></appmsg></msg>`,
			"微信支付收款1.01元"},
		{`<msg><appmsg><des>没有标题</des></appmsg></msg>`, ""},
		{`<msg></msg>`, ""},
	}
	for _, c := range cases {
		if got := xmlTitle(c.xml); got != c.want {
			t.Errorf("xmlTitle(%q) = %q, want %q", c.xml, got, c.want)
		}
	}
}

func TestKeywordMatch(t *testing.T) {
	// 默认关键词全部命中的典型文案
	texts := []string{
		"微信支付收款0.10元(朋友到店)",
		"微信收款助手：你已成功收款1.01元",
		"二维码赞赏到账 5.00元",
		"【店员消息】张三通过扫码向你付款0.50元",
		"收款单到账 2.00元",
		"个人收款码到账 1.00元",
		"收款通知：金额 3.00 元",
		"微信收款商业版：收款成功",
		"对外收款 8.88 元",
		"收钱码收款 0.50 元",
		"店员通：新收款",
		"已转入余额 12.34 元",
		"你已成功收款 1.01 元",
		"通过扫码向你付款 0.50 元",
		"成功收款 9.99 元",
	}
	want := map[string]bool{
		"微信支付收款0.10元(朋友到店)":       true,
		"微信收款助手：你已成功收款1.01元":     true,
		"二维码赞赏到账 5.00元":            true,
		"【店员消息】张三通过扫码向你付款0.50元": true,
		"收款单到账 2.00元":              true,
		"个人收款码到账 1.00元":            true,
		"收款通知：金额 3.00 元":           true,
		"微信收款商业版：收款成功":           true,
		"对外收款 8.88 元":              true,
		"收钱码收款 0.50 元":             true,
		"店员通：新收款":                 true,
		"已转入余额 12.34 元":            true,
		"你已成功收款 1.01 元":            true,
		"通过扫码向你付款 0.50 元":         true,
		"成功收款 9.99 元":              true,
	}

	for _, txt := range texts {
		matched := false
		for _, kw := range defaultKeywords {
			if containsStr(txt, kw) {
				matched = true
				break
			}
		}
		if matched != want[txt] {
			t.Errorf("关键词判定错误: %q matched=%v want=%v", txt, matched, want[txt])
		}
		if got := extractAmount(txt); got <= 0 {
			t.Logf("提示: %q 未提取到金额", txt)
		}
	}
}

func containsStr(s, sub string) bool {
	return len(sub) > 0 && len(s) >= len(sub) &&
		indexOf(s, sub) >= 0
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
