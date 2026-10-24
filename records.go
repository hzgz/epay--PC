package main

// 收款记录持久化：重启不丢数据。
// 注意：这里只保存「本程序运行时亲眼抓到的消息」，
// 与读取微信历史聊天记录是两回事。

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	recordsMax  = 2000             // 最多保留多少条
	recordsFile = "records.json"
)

var (
	recMu   sync.Mutex
	recDirty bool
	recPath  string
)

func recordsPath() string {
	if recPath != "" {
		return recPath
	}
	exe, err := os.Executable()
	dir := "."
	if err == nil {
		dir = filepath.Dir(exe)
	}
	recPath = filepath.Join(dir, recordsFile)
	return recPath
}

// loadRecords 启动时把上次的记录读回来
func loadRecords() []MsgView {
	recMu.Lock()
	defer recMu.Unlock()
	b, err := os.ReadFile(recordsPath())
	if err != nil {
		return nil
	}
	var out []MsgView
	if json.Unmarshal(b, &out) != nil {
		return nil
	}
	return out
}

// saveRecords 把记录写到磁盘（调用方负责加锁或传入副本）
func saveRecords(items []MsgView) {
	b, err := json.MarshalIndent(items, "", " ")
	if err != nil {
		return
	}
	tmp := recordsPath() + ".tmp"
	if os.WriteFile(tmp, b, 0644) != nil {
		return
	}
	os.Rename(tmp, recordsPath())
}

// markRecordsDirty 标记有待落盘的数据
func markRecordsDirty() {
	recMu.Lock()
	recDirty = true
	recMu.Unlock()
}

// recordsFlusher 后台定时落盘：合并短时间内的多次写入，避免频繁 IO
func recordsFlusher(stop <-chan struct{}) {
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-stop:
			return
		case <-tick.C:
			recMu.Lock()
			dirty := recDirty
			recDirty = false
			recMu.Unlock()
			if !dirty {
				continue
			}
			// 拿一份快照再写，避免长时间持锁
			eng.mu.Lock()
			items := make([]MsgView, len(eng.history))
			copy(items, eng.history)
			eng.mu.Unlock()
			saveRecords(items)
		}
	}
}

// flushRecordsNow 退出前立即落盘
func flushRecordsNow() {
	eng.mu.Lock()
	items := make([]MsgView, len(eng.history))
	copy(items, eng.history)
	eng.mu.Unlock()
	saveRecords(items)
	recMu.Lock()
	recDirty = false
	recMu.Unlock()
}

// msgID 记录的唯一标识（时间 + 收发方 + 内容）
func msgID(v *MsgView) string {
	return fmt.Sprintf("%x", hashMsg(v.From+"|"+v.To+"|"+v.Content, v.Time))
}

// deleteRecords 删除指定 id 的记录，返回删除条数
func (e *Engine) deleteRecords(ids map[string]bool) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]MsgView, 0, len(e.history))
	removed := 0
	for i := range e.history {
		v := e.history[i]
		if v.ID == "" {
			v.ID = msgID(&v)
		}
		if ids[v.ID] {
			removed++
			continue
		}
		out = append(out, v)
	}
	if removed > 0 {
		e.history = out
		markRecordsDirty()
	}
	return removed
}

