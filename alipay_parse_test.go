package main

import (
	"testing"
)

func TestParseAlipayBills_MultiDiv(t *testing.T) {
	html := `
<div class="record-item">
  <span class="time">2026-10-17 19:02:31</span>
  <span class="memo">Kudi-转账</span>
  <span class="amount">0.10</span>
  <span class="trade-no">20261017200040011100780049961241</span>
</div>
<div class="record-item">
  <span class="time">2026-10-17 19:03:44</span>
  <span class="memo">收钱码收款</span>
  <span class="amount">0.20</span>
  <span class="trade-no">2026101723001461781402492527</span>
</div>
<div class="record-item">
  <span class="time">2026-10-20 23:56:57</span>
  <span class="memo">收钱码收款</span>
  <span class="amount">1.00</span>
  <span class="trade-no">2026102023001461781422970877</span>
</div>
<p>账户 PID 2088123456789012</p>`

	got := parseAlipayBillHTML(html)
	if len(got) != 3 {
		t.Fatalf("期望 3 条，实际 %d 条: %+v", len(got), got)
	}
	want := []struct {
		no  string
		amt float64
	}{
		{"20261017200040011100780049961241", 0.10},
		{"2026101723001461781402492527", 0.20},
		{"2026102023001461781422970877", 1.00},
	}
	for i, w := range want {
		if got[i].TradeNo != w.no {
			t.Errorf("[%d] 交易号 = %s，期望 %s", i, got[i].TradeNo, w.no)
		}
		if got[i].Amount != w.amt {
			t.Errorf("[%d] 金额 = %.2f，期望 %.2f", i, got[i].Amount, w.amt)
		}
	}
	for _, b := range got {
		if b.TradeNo == "2088123456789012" {
			t.Error("PID 被误当交易号")
		}
	}
}

func TestParseAlipayBills_Table(t *testing.T) {
	html := `<table>
<tr><td>2026-10-17 19:02:31</td><td>Kudi-转账</td><td>￥0.10</td>
    <td><a href="/record/detail.htm?tradeNo=20261017200040011100780049961241">详情</a></td></tr>
<tr><td>2026-10-17 19:03:44</td><td>收钱码收款</td><td>0.10元</td>
    <td>交易号 2026101723001461781402492527</td></tr>
</table>`
	got := parseAlipayBillHTML(html)
	if len(got) != 2 {
		t.Fatalf("期望 2 条，实际 %d 条: %+v", len(got), got)
	}
	if got[0].Memo != "Kudi-转账" {
		t.Errorf("说明 = %q，期望 Kudi-转账", got[0].Memo)
	}
	if got[1].Time != "2026-10-17 19:03:44" {
		t.Errorf("时间 = %q", got[1].Time)
	}
}

func TestParseAlipayBills_Empty(t *testing.T) {
	if got := parseAlipayBillHTML(""); len(got) != 0 {
		t.Errorf("空页面应该返回 0 条，实际 %d", len(got))
	}
	if got := parseAlipayBillHTML("<html>请登录</html>"); len(got) != 0 {
		t.Errorf("登录页应该返回 0 条，实际 %d", len(got))
	}
}
