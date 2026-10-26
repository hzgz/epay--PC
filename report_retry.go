package main

// 上报重试：只在「网络抖动」时重试（请求根本没拿到 HTTP 响应）。
// 平台明确返回了内容（任何状态码、任何业务错误）都视为最终结果，不再重试。
// 最多重试 3 次，每次间隔 5 秒。

import (
	"fmt"
	"sync"
	"time"
)

const (
	reportRetryMax      = 3
	reportRetryInterval = 5 * time.Second
)

type retryJob struct {
	m        *ChatMsg
	attempts int
	nextAt   time.Time
	lastErr  string
}

var (
	retryMu   sync.Mutex
	retryJobs = map[string]*retryJob{}
	retryOnce sync.Once
)

// reportRetryable 只有传输层失败才值得重试：
// URL 已经拼好（说明配置没问题）、没拿到响应、没有状态码。
func reportRetryable(res ReportResult) bool {
	return res.URL != "" && !res.Delivered && res.Code == 0 && res.Err != ""
}

func retryKey(m *ChatMsg) string {
	return fmt.Sprintf("%s|%s|%s|%d", m.From, m.To, m.Content, m.Time)
}

// enqueueReportRetry 上报因网络问题失败时排队重试
func enqueueReportRetry(m *ChatMsg, res ReportResult) {
	k := retryKey(m)
	retryMu.Lock()
	if _, ok := retryJobs[k]; ok {
		retryMu.Unlock()
		return
	}
	retryJobs[k] = &retryJob{m: m, nextAt: time.Now().Add(reportRetryInterval), lastErr: res.Err}
	retryMu.Unlock()
	logWarn("callback", "上报网络异常，%d 秒后重试（最多 %d 次）：%s",
		int(reportRetryInterval/time.Second), reportRetryMax, res.Err)
	startReportRetryWorker()
}

func startReportRetryWorker() {
	retryOnce.Do(func() {
		go func() {
			t := time.NewTicker(time.Second)
			defer t.Stop()
			for range t.C {
				runDueRetries(time.Now())
			}
		}()
	})
}

func runDueRetries(now time.Time) {
	type due struct {
		key string
		job retryJob
	}
	var list []due
	retryMu.Lock()
	for k, j := range retryJobs {
		if now.Before(j.nextAt) {
			continue
		}
		list = append(list, due{key: k, job: *j})
	}
	retryMu.Unlock()

	for _, d := range list {
		res := Report(d.job.m)
		ok := res.Delivered && res.OK
		attempt := d.job.attempts + 1
		if ok {
			retryMu.Lock()
			delete(retryJobs, d.key)
			retryMu.Unlock()
			updateRecordCallback(d.job.m, true, res)
			logInfo("callback", "重试成功（第 %d 次）", attempt)
			continue
		}
		if reportRetryable(res) && attempt < reportRetryMax {
			retryMu.Lock()
			if j, ok2 := retryJobs[d.key]; ok2 {
				j.attempts = attempt
				j.nextAt = time.Now().Add(reportRetryInterval)
				j.lastErr = res.Err
			}
			retryMu.Unlock()
			logWarn("callback", "第 %d 次重试仍失败：%s", attempt, res.Err)
			continue
		}
		retryMu.Lock()
		delete(retryJobs, d.key)
		retryMu.Unlock()
		if res.Err == "" && !res.Delivered {
			res.Err = d.job.lastErr
		}
		updateRecordCallback(d.job.m, false, res)
		logError("callback", "重试结束，仍失败：%s", res.Err)
	}
}

// updateRecordCallback 回写同一条记录的失败/成功状态（不新增记录）
func updateRecordCallback(m *ChatMsg, ok bool, res ReportResult) {
	e := eng
	if e == nil {
		return
	}
	rawID := fmt.Sprintf("%d", m.MsgID)
	e.mu.Lock()
	defer e.mu.Unlock()
	for i := range e.history {
		v := &e.history[i]
		if v.RawID != rawID || v.Time != m.Time || v.Account != m.To {
			continue
		}
		v.Reported = ok
		if ok {
			v.CallbackStatus = "已回调"
			v.Err = ""
		} else {
			v.CallbackStatus = "回调失败"
			if res.Err != "" {
				v.Err = res.Err
			}
		}
		if res.URL != "" {
			v.CallbackURL = res.URL
		}
		if res.Body != "" {
			v.CallbackBody = res.Body
		} else if !ok && res.Err != "" {
			v.CallbackBody = res.Err
		}
		markRecordsDirty()
		return
	}
}
