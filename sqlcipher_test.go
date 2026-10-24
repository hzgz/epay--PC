package main

import (
	"fmt"
	"testing"
	"time"
)

// 验证自实现的 SQLCipher 第一页 HMAC 与真实数据库匹配
func TestSQLCipherHMAC(t *testing.T) {
	root := WeChatRoot()
	if root == "" {
		t.Skip("没有微信数据目录")
	}
	var target WxAccount
	for _, a := range ScanWxAccounts() {
		if a.HasBiz && a.Wxid == "wxid_7fr1od8eqymv22" {
			target = a
			break
		}
	}
	if target.Wxid == "" {
		for _, a := range ScanWxAccounts() {
			if a.HasBiz {
				target = a
				break
			}
		}
	}
	if target.Wxid == "" {
		t.Skip("没有带 biz 库的账号")
	}

	page1, err := readFirstPage(target.BizDB)
	if err != nil {
		t.Fatalf("读首页失败: %v", err)
	}
	t.Logf("首页 %d B  salt=%x", len(page1), page1[:16])

	const known = "da9205a940494db897e7664d126c68faaff472aaff0e4b77aa4a4b6390edde53"
	wrong := "0000000000000000000000000000000000000000000000000000000000000000"

	start := time.Now()
	ok := scVerifyPage1(mustHex(known), page1)
	elapsed := time.Since(start)
	t.Logf("已知密钥验证: %v，耗时 %v", ok, elapsed)
	if !ok {
		t.Fatal("已知密钥没通过 HMAC 验证 —— 参数不对")
	}

	start = time.Now()
	bad := scVerifyPage1(mustHex(wrong), page1)
	t.Logf("错误密钥验证: %v，耗时 %v", bad, time.Since(start))
	if bad {
		t.Fatal("全 0 密钥竟然通过")
	}

	// 与 WCDB 对比速度
	start = time.Now()
	wcdbOK := verifyKeyFast(known, target.BizDB)
	wcdbTime := time.Since(start)
	t.Logf("WCDB 验证: %v，耗时 %v", wcdbOK, wcdbTime)

	if wcdbOK != ok {
		t.Fatalf("两种方式结论不一致: HMAC=%v WCDB=%v", ok, wcdbOK)
	}
	t.Logf("加速比: %.1fx", float64(wcdbTime)/float64(elapsed))
}

// 并行验证吞吐
func TestSQLCipherParallel(t *testing.T) {
	root := WeChatRoot()
	if root == "" {
		t.Skip("没有微信数据目录")
	}
	var target WxAccount
	for _, a := range ScanWxAccounts() {
		if a.HasBiz && a.Wxid == "wxid_7fr1od8eqymv22" {
			target = a
			break
		}
	}
	if target.Wxid == "" {
		t.Skip("找不到主账号")
	}
	page1, err := readFirstPage(target.BizDB)
	if err != nil {
		t.Skip("读首页失败")
	}
	const known = "da9205a940494db897e7664d126c68faaff472aaff0e4b77aa4a4b6390edde53"

	// 先确认单线程基准（同一份 page1）
	if !scVerifyPage1(mustHex(known), page1) {
		t.Fatal("单线程基准失败，page1 或密钥有问题")
	}
	t.Log("单线程基准 OK")

	// 100 个候选，一半正确一半错误，8 线程
	const total = 100
	start := time.Now()
	type res struct{ idx int; ok bool }
	results := make(chan res, total)
	sem := make(chan struct{}, 8)
	for i := 0; i < total; i++ {
		sem <- struct{}{}
		go func(idx int) {
			defer func() { <-sem }()
			var key []byte
			if idx%2 == 0 {
				key = mustHex(known)
			} else {
				key = mustHex("1111111111111111111111111111111111111111111111111111111111111111")
			}
			results <- res{idx, scVerifyPage1(key, page1)}
		}(i)
	}
	correct, falsePos := 0, 0
	for i := 0; i < total; i++ {
		r := <-results
		if r.idx%2 == 0 && r.ok {
			correct++
		}
		if r.idx%2 == 1 && r.ok {
			falsePos++
		}
	}
	elapsed := time.Since(start)
	t.Logf("并行 %d 个候选耗时 %v（平均 %v/个）", total, elapsed, elapsed/total)
	t.Logf("正确密钥通过 %d/50，错误密钥误判 %d/50", correct, falsePos)
	if correct != 50 {
		t.Fatalf("正确密钥通过数不对: %d != 50", correct)
	}
	if falsePos != 0 {
		t.Errorf("错误密钥误判 %d 个", falsePos)
	}
	// 预估 2800 个候选
	est := elapsed * 2800 / total
	t.Logf("按此速度 2800 个候选约需 %v", est)
}

func mustHex(s string) []byte {
	out := make([]byte, len(s)/2)
	for i := 0; i < len(out); i++ {
		var b byte
		fmt.Sscanf(s[i*2:i*2+2], "%02x", &b)
		out[i] = b
	}
	return out
}





