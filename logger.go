package main

// 运行日志：级别 + 模块 + 环形缓冲，供界面查询

import (
	"fmt"
	"sync"
	"time"
)

type LogEntry struct {
	Time   string `json:"time"`
	Level  string `json:"level"`  // DEBUG / INFO / WARN / ERROR
	Module string `json:"module"` // monitor / callback / config / system / inject
	Msg    string `json:"msg"`
	Seq    int    `json:"seq"`
}

const logMax = 1000

var (
	logMu  sync.Mutex
	logBuf []LogEntry
	logSeq int
)

// logL 记录一条结构化日志：既打到控制台，也进环形缓冲
func logL(level, module, format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	ts := time.Now()

	logMu.Lock()
	logSeq++
	e := LogEntry{
		Time:   ts.Format("2006/01/02 15:04:05"),
		Level:  level,
		Module: module,
		Msg:    msg,
		Seq:    logSeq,
	}
	// 头插，最新的在最前
	logBuf = append([]LogEntry{e}, logBuf...)
	if len(logBuf) > logMax {
		logBuf = logBuf[:logMax]
	}
	logMu.Unlock()

	// GUI 模式下没有控制台，Printf 会被系统忽略；加 recover 防止极端情况
	func() {
		defer func() { recover() }()
		fmt.Printf("[%s] [%s] %s\n", ts.Format("15:04:05"), level, msg)
	}()
}

func logInfo(module, format string, args ...interface{})  { logL("INFO", module, format, args...) }
func logWarn(module, format string, args ...interface{})  { logL("WARN", module, format, args...) }
func logError(module, format string, args ...interface{}) { logL("ERROR", module, format, args...) }
func logDebug(module, format string, args ...interface{}) { logL("DEBUG", module, format, args...) }

// logsCopy 按条件取日志
func logsCopy(level, module string, limit int) []LogEntry {
	logMu.Lock()
	defer logMu.Unlock()
	out := make([]LogEntry, 0, len(logBuf))
	for _, e := range logBuf {
		if level != "" && e.Level != level {
			continue
		}
		if module != "" && e.Module != module {
			continue
		}
		out = append(out, e)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out
}

func logsClear() {
	logMu.Lock()
	logBuf = nil
	logMu.Unlock()
}

