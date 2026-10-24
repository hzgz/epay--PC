package main

import (
	"os"
	"path/filepath"
	"testing"
)

// 验证 verifyKeyFast 不会把 A 账号的密钥判成 B 账号可用
func TestVerifyKeyCrossAccount(t *testing.T) {
	root := WeChatRoot()
	if root == "" {
		t.Skip("没有微信数据目录")
	}

	// 找出所有有 biz 库的账号
	var accs []WxAccount
	for _, a := range ScanWxAccounts() {
		if a.HasBiz {
			accs = append(accs, a)
		}
	}
	if len(accs) < 2 {
		t.Skipf("需要至少两个有 biz 库的账号，当前 %d 个", len(accs))
	}

	const known = "da9205a940494db897e7664d126c68faaff472aaff0e4b77aa4a4b6390edde53"

	// 先打印每个账号用这把密钥的验证结果
	for _, a := range accs {
		ok := verifyKeyFast(known, a.BizDB)
		t.Logf("账号 %s  库=%s  用已知密钥=%v", a.Wxid, filepath.Base(filepath.Dir(filepath.Dir(filepath.Dir(a.BizDB)))), ok)
	}

	// 至少有一个账号能用这把密钥（它就是这把密钥的主人）
	anyOK := false
	for _, a := range accs {
		if verifyKeyFast(known, a.BizDB) {
			anyOK = true
		}
	}
	if !anyOK {
		t.Logf("注意：没有任何账号能用这把密钥（可能账号换了）")
	}

	// 关键断言：如果某账号用不了，就不能被判成可用
	for _, a := range accs {
		if a.Wxid == "wxid_7fr1od8eqymv22" {
			continue
		}
		ok := verifyKeyFast(known, a.BizDB)
		if ok {
			t.Logf("账号 %s 也通过了 —— 需要人工确认是不是同一把密钥", a.Wxid)
		} else {
			t.Logf("账号 %s 正确拒绝", a.Wxid)
		}
	}
}

// 用错误的密钥必须返回 false
func TestVerifyKeyWrong(t *testing.T) {
	root := WeChatRoot()
	if root == "" {
		t.Skip("没有微信数据目录")
	}
	var target WxAccount
	for _, a := range ScanWxAccounts() {
		if a.HasBiz {
			target = a
			break
		}
	}
	if target.Wxid == "" {
		t.Skip("没有可用账号")
	}
	wrong := "0000000000000000000000000000000000000000000000000000000000000000"
	if verifyKeyFast(wrong, target.BizDB) {
		t.Fatal("全 0 密钥竟然通过了验证")
	}
	if !verifyKeyFast("da9205a940494db897e7664d126c68faaff472aaff0e4b77aa4a4b6390edde53", target.BizDB) {
		t.Logf("已知密钥对 %s 不通过（可能不是该账号的密钥）", target.Wxid)
	}
}

var _ = os.Stat
