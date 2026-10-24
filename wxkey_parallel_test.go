package main

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// 测试多线程并行调用 wxtool_open 是否稳定
// 背景：之前 8 线程并行验证全部失败，需要确认是 WCDB 不支持并发，还是我们的用法有问题
func TestParallelOpen(t *testing.T) {
	root := WeChatRoot()
	if root == "" {
		t.Skip("没有微信数据目录")
	}
	var good, bad WxAccount
	for _, a := range ScanWxAccounts() {
		if !a.HasBiz {
			continue
		}
		if a.Wxid == "wxid_7fr1od8eqymv22" {
			good = a
		} else if bad.Wxid == "" {
			bad = a
		}
	}
	if good.Wxid == "" {
		t.Skip("找不到已知密钥的账号")
	}

	const known = "da9205a940494db897e7664d126c68faaff472aaff0e4b77aa4a4b6390edde53"
	wrong := "0000000000000000000000000000000000000000000000000000000000000000"

	// 先确认单线程基准
	if !verifyKeyFast(known, good.BizDB) {
		t.Fatal("单线程：正确密钥竟然失败")
	}
	if verifyKeyFast(wrong, good.BizDB) {
		t.Fatal("单线程：错误密钥竟然通过")
	}
	t.Log("单线程基准正常")

	// 并行：一半正确一半错误，各跑 N 次，看结果是否稳定
	const workers = 4
	const rounds = 20
	var goodOK, goodFail, badOK, badFail int64
	var wg sync.WaitGroup

	start := time.Now()
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := 0; i < rounds; i++ {
				if (id+i)%2 == 0 {
					if verifyKeyFast(known, good.BizDB) {
						atomic.AddInt64(&goodOK, 1)
					} else {
						atomic.AddInt64(&goodFail, 1)
					}
				} else {
					if verifyKeyFast(wrong, good.BizDB) {
						atomic.AddInt64(&badOK, 1)
					} else {
						atomic.AddInt64(&badFail, 1)
					}
				}
			}
		}(w)
	}
	wg.Wait()
	elapsed := time.Since(start)

	total := int64(workers * rounds)
	t.Logf("并行 %d 线程 × %d 次 = %d 次，耗时 %v（平均 %v/次）",
		workers, rounds, total, elapsed, elapsed/time.Duration(total))
	t.Logf("正确密钥：通过 %d / 失败 %d", goodOK, goodFail)
	t.Logf("错误密钥：通过 %d / 失败 %d  ← 通过数必须是 0", badOK, badFail)

	if goodFail > 0 {
		t.Errorf("正确密钥有 %d 次被误判失败，说明并发下不稳定", goodFail)
	}
	if badOK > 0 {
		t.Errorf("错误密钥有 %d 次被误判通过，说明并发下结果不可信", badOK)
	}
}
