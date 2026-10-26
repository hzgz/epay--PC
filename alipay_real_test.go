package main

import (
	"os"
	"testing"
)

func TestParseRealAlipayBill(t *testing.T) {
	raw, err := os.ReadFile(`C:\Users\Administrator\Desktop\pcjk\bill_real.html`)
	if err != nil {
		t.Skip("没有真实账单页，跳过")
	}
	// 走和线上一样的路径：GBK -> UTF-8 -> 解析
	html := string(alipayToUTF8(raw))

	bills := parseAlipayBillHTML(html)
	t.Logf("解析出 %d 条收款", len(bills))
	for i, b := range bills {
		t.Logf("  [%d] %s  %.2f  %s  %q",
			i, b.TradeNo, b.Amount, b.Time, b.Memo)
		if len(b.TradeNo) < 16 {
			t.Errorf("交易号太短: %q", b.TradeNo)
		}
		if b.Amount <= 0 {
			t.Errorf("金额异常: %.2f", b.Amount)
		}
		if b.Time == "" {
			t.Errorf("时间为空: %s", b.TradeNo)
		}
		if b.Memo == "" || b.Memo == "\uFFFD" {
			t.Errorf("说明为空或乱码: %q", b.Memo)
		}
	}
	if len(bills) == 0 {
		t.Fatal("一条都没解析出来")
	}
}
