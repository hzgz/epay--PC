package main

// 支付宝「只上报新入账」的筛选规则测试。
//
// 对应需求：页面上的历史账单既不进收款记录也不上报；
// 只有首次接入之后新出现的、且还在时效窗口内的进账才算数。

import (
	"testing"
	"time"
)

func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	v, err := time.ParseInLocation(alipayBillTimeLayout, s, time.Local)
	if err != nil {
		t.Fatalf("bad time %q: %v", s, err)
	}
	return v
}

// 首次接入：只建立基线，一笔都不处理（不写收款记录、不上报）
func TestAlipaySelectFirstRunBaselinesOnly(t *testing.T) {
	now := mustTime(t, "2026-10-25 19:00:00")
	bills := []AlipayBill{
		{TradeNo: "A3", Amount: 0.02, Time: "2026-10-25 18:59:30", Memo: "收钱码收款"},
		{TradeNo: "A2", Amount: 1.00, Time: "2026-10-25 12:00:00", Memo: "收钱码收款"},
		{TradeNo: "A1", Amount: 5.00, Time: "2026-10-20 09:00:00", Memo: "收钱码收款"},
	}

	picked, seen, wm, st := alipaySelectNew(bills, map[string]bool{}, "", now, 30*time.Minute)

	if len(picked) != 0 {
		t.Fatalf("首次接入不该挑出任何账单，得到 %d 笔", len(picked))
	}
	if st.FirstBaseline != 3 || st.Picked != 0 {
		t.Fatalf("统计不对: %+v", st)
	}
	if wm != "2026-10-25 18:59:30" {
		t.Fatalf("基线应取页面最新一笔，得到 %q", wm)
	}
	if len(seen) != 3 {
		t.Fatalf("三笔交易号都该记为已处理，得到 %d", len(seen))
	}
}

// 已建立基线后：只有晚于基线的才算新入账
func TestAlipaySelectOnlyNewerThanWatermark(t *testing.T) {
	now := mustTime(t, "2026-10-25 19:00:00")
	bills := []AlipayBill{
		{TradeNo: "NEW", Amount: 0.01, Time: "2026-10-25 18:59:50", Memo: "收钱码收款"},
		{TradeNo: "SAME", Amount: 0.02, Time: "2026-10-25 18:59:30", Memo: "收钱码收款"},
		{TradeNo: "OLD", Amount: 9.99, Time: "2026-10-25 18:00:00", Memo: "收钱码收款"},
	}

	picked, _, wm, st := alipaySelectNew(bills, map[string]bool{}, "2026-10-25 18:59:30", now, 30*time.Minute)

	if len(picked) != 1 || picked[0].TradeNo != "NEW" {
		t.Fatalf("应只挑出 NEW，实际 %+v", picked)
	}
	if st.OlderThanWatermark != 2 {
		t.Fatalf("两笔不晚于基线的应算旧账单，统计 %+v", st)
	}
	if wm != "2026-10-25 18:59:50" {
		t.Fatalf("基线应推进到最新一笔，得到 %q", wm)
	}
}

// 超时窗口：比基线新、但已经很旧的账单（停机期间翻出来的）不上报
func TestAlipaySelectSkipsStaleBills(t *testing.T) {
	now := mustTime(t, "2026-10-25 19:00:00")
	bills := []AlipayBill{
		{TradeNo: "FRESH", Amount: 0.01, Time: "2026-10-25 18:59:00", Memo: "收钱码收款"},
		{TradeNo: "STALE", Amount: 0.03, Time: "2026-10-25 17:00:00", Memo: "收钱码收款"},
	}

	picked, seen, _, st := alipaySelectNew(bills, map[string]bool{},
		"2026-10-25 16:00:00", now, 30*time.Minute)

	if len(picked) != 1 || picked[0].TradeNo != "FRESH" {
		t.Fatalf("只应挑出 FRESH，实际 %+v", picked)
	}
	if st.TooOld != 1 {
		t.Fatalf("STALE 应记为超时，统计 %+v", st)
	}
	// 跳过的也要记账，避免每轮重复评估
	if len(seen) != 2 {
		t.Fatalf("两笔交易号都该记为已处理，得到 %d", len(seen))
	}
}

// 已处理过的交易号不再重复处理，基线不会倒退
func TestAlipaySelectDedupAndMonotonicWatermark(t *testing.T) {
	now := mustTime(t, "2026-10-25 19:00:00")
	bills := []AlipayBill{
		{TradeNo: "DONE", Amount: 0.01, Time: "2026-10-25 18:59:00", Memo: "收钱码收款"},
		{TradeNo: "OLD", Amount: 0.02, Time: "2026-10-25 18:00:00", Memo: "收钱码收款"},
	}
	seen := map[string]bool{"DONE": true}

	picked, fresh, wm, st := alipaySelectNew(bills, seen, "2026-10-25 18:59:30", now, 30*time.Minute)

	if len(picked) != 0 {
		t.Fatalf("没有新入账，实际挑出 %+v", picked)
	}
	if st.AlreadyKnown != 1 {
		t.Fatalf("DONE 应算已知，统计 %+v", st)
	}
	if len(fresh) != 1 || fresh[0] != "OLD" {
		t.Fatalf("只有 OLD 是新见到的交易号，实际 %v", fresh)
	}
	// 页面最新一笔 (18:59:00) 比基线 (18:59:30) 老，基线必须保持不动
	if wm != "2026-10-25 18:59:30" {
		t.Fatalf("基线不应倒退，得到 %q", wm)
	}
}

// 时间解析不出来时宁可上报（平台会判重），也不能悄悄漏单
func TestAlipaySelectMissingTimeStillReported(t *testing.T) {
	now := mustTime(t, "2026-10-25 19:00:00")
	bills := []AlipayBill{
		{TradeNo: "NOTIME", Amount: 0.01, Time: "", Memo: "收钱码收款"},
	}

	picked, _, wm, _ := alipaySelectNew(bills, map[string]bool{},
		"2026-10-25 18:00:00", now, 30*time.Minute)

	if len(picked) != 1 || picked[0].TradeNo != "NOTIME" {
		t.Fatalf("时间缺失的账单应照常上报，实际 %+v", picked)
	}
	if wm != "2026-10-25 18:00:00" {
		t.Fatalf("时间全缺失时基线不该变，得到 %q", wm)
	}
}

// 页面时间超前（支付宝偶尔返回未来时间）不能把基线顶到未来
func TestAlipaySelectClampsFutureTime(t *testing.T) {
	now := mustTime(t, "2026-10-25 19:00:00")
	bills := []AlipayBill{
		{TradeNo: "FUTURE", Amount: 0.01, Time: "2026-10-26 10:00:00", Memo: "收钱码收款"},
	}

	_, _, wm, _ := alipaySelectNew(bills, map[string]bool{}, "", now, 30*time.Minute)

	if wm != "2026-10-25 19:00:00" {
		t.Fatalf("基线应被夹到当前时间，得到 %q", wm)
	}
}
