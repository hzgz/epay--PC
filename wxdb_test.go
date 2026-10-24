package main

import (
	"os"
	"testing"
)

// 真实环境测试：读取微信收款助手 + 微信支付的消息
func TestWxDBReal(t *testing.T) {
	key := os.Getenv("WX_KEY")
	if key == "" {
		key = "da9205a940494db897e7664d126c68faaff472aaff0e4b77aa4a4b6390edde53"
	}

	// 1. 扫描账号
	accs := ScanWxAccounts()
	t.Logf("发现 %d 个微信账号", len(accs))
	if len(accs) == 0 {
		t.Skip("本机没有微信数据")
	}
	want := os.Getenv("WX_WXID")
	var acc *WxAccount
	for i := range accs {
		t.Logf("  %s  biz=%v  dir=%s", accs[i].Wxid, accs[i].HasBiz, accs[i].Dir)
		if accs[i].HasBiz && acc == nil {
			acc = &accs[i]
		}
		if want != "" && accs[i].Wxid == want && accs[i].HasBiz {
			acc = &accs[i]
		}
	}
	if acc == nil {
		t.Skip("没有 biz_message_0.db")
	}

	// 2. 打开数据库
	db, err := OpenWxDB(acc.BizDB, key)
	if err != nil {
		t.Fatalf("打开失败: %v", err)
	}
	defer db.Close()
	t.Logf("已解密: %s", acc.BizDB)

	// 3. 表名规则验证
	tables, err := db.Tables()
	if err != nil {
		t.Fatalf("列表失败: %v", err)
	}
	t.Logf("表数量: %d", len(tables))
	have := map[string]bool{}
	for _, n := range tables {
		have[n] = true
	}

	// 4. 读取收款助手和微信支付
	targets := map[string]string{
		"微信收款助手": "gh_f0a92aa7146c",
		"微信支付":   "gh_3dfda90e39d6",
	}
	for label, gh := range targets {
		tbl := GhTable(gh)
		if !have[tbl] {
			t.Logf("[%s] 表 %s 不存在", label, tbl)
			continue
		}
		n, _ := db.Count(tbl)
		t.Logf("\n=== %s (%s)  表=%s  共 %d 条 ===", label, gh, tbl, n)

		msgs, err := db.Messages(tbl, 3, 0, false)
		if err != nil {
			t.Logf("  读取失败: %v", err)
			continue
		}
		for i, m := range msgs {
			gm := ParseGhContent(m.Content)
			t.Logf("  [%d] local_id=%d  time=%d  zstd=%v", i+1, m.LocalID, m.CreateAt, m.IsZstd)
			t.Logf("      title : %s", truncateStr(gm.Title, 50))
			if gm.Des != "" {
				t.Logf("      des   : %s", truncateStr(gm.Des, 60))
			}
			t.Logf("      amount: %.2f", gm.Amount)
		}
	}
}

func truncateStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}




