package main

// 微信消息解码：zstd 解压 + XML 提取

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
	"sync"

	"github.com/klauspost/compress/zstd"
)

var (
	zstdOnce sync.Once
	zstdDec  *zstd.Decoder
	zstdErr  error
)

// decompress 解压 zstd 数据（原始字节或 hex 字符串）
func decompress(data []byte) ([]byte, error) {
	if len(data) >= 4 && data[0] == 0x28 && data[1] == 0xb5 && data[2] == 0x2f && data[3] == 0xfd {
		zstdOnce.Do(func() {
			zstdDec, zstdErr = zstd.NewReader(nil,
				zstd.WithDecoderMaxMemory(64<<20),
				zstd.WithDecoderConcurrency(1))
		})
		if zstdErr != nil {
			return nil, zstdErr
		}
		return zstdDec.DecodeAll(data, nil)
	}
	return data, nil
}

// DecodeContent 把消息内容解压成文本
// 返回 (文本, 是否为 XML)
func DecodeContent(hexOrText string) (string, bool, error) {
	s := strings.TrimSpace(hexOrText)
	if s == "" {
		return "", false, nil
	}
	// zstd hex（微信 4.x 常见）
	if strings.HasPrefix(strings.ToLower(s), "28b52ffd") && len(s)%2 == 0 {
		raw, err := hex.DecodeString(s)
		if err == nil {
			out, err := decompress(raw)
			if err != nil {
				return "", false, fmt.Errorf("zstd 解压失败: %w", err)
			}
			return string(out), true, nil
		}
	}
	// 直接就是 XML/文本
	if strings.HasPrefix(s, "<") {
		return s, true, nil
	}
	return s, false, nil
}

// ---------- XML 字段提取 ----------

var (
	reTitle = regexp.MustCompile(`(?is)<title>\s*(?:<!\[CDATA\[)?(.*?)(?:\]\]>)?\s*</title>`)
	reDes   = regexp.MustCompile(`(?is)<des>\s*(?:<!\[CDATA\[)?(.*?)(?:\]\]>)?\s*</des>`)
	reURL   = regexp.MustCompile(`(?is)<url>\s*(?:<!\[CDATA\[)?(.*?)(?:\]\]>)?\s*</url>`)
	reText  = regexp.MustCompile(`(?is)<text>\s*(?:<!\[CDATA\[)?(.*?)(?:\]\]>)?\s*</text>`)
)

// GhMessage 从公众号消息里提取的结构化数据
type GhMessage struct {
	Title   string
	Des     string
	URL     string
	Amount  float64
	Raw     string // 原始存储值（可能是 hex）
	Decoded string // 解压后的可读文本（XML 原文）
	IsXML   bool
}

// ParseGhContent 解析公众号消息正文
func ParseGhContent(content string) GhMessage {
	out := GhMessage{Raw: content}
	txt, isXML, _ := DecodeContent(content)
	out.IsXML = isXML
	out.Decoded = txt
	if !isXML {
		out.Title = strings.TrimSpace(txt)
		out.Amount = extractAmount(out.Title)
		return out
	}

	get := func(re *regexp.Regexp) string {
		m := re.FindStringSubmatch(txt)
		if len(m) < 2 {
			return ""
		}
		s := m[1]
		s = strings.ReplaceAll(s, "<![CDATA[", "")
		s = strings.ReplaceAll(s, "]]>", "")
		return strings.TrimSpace(s)
	}

	out.Title = get(reTitle)
	out.Des = get(reDes)
	out.URL = get(reURL)
	if out.Title == "" {
		out.Title = get(reText)
	}
	if out.Title == "" {
		// 纯文本 XML 之外的内容
		out.Title = strings.TrimSpace(stripTags(txt))
	}
	out.Amount = extractAmount(out.Title)
	if out.Amount == 0 && out.Des != "" {
		out.Amount = extractAmount(out.Des)
	}
	return out
}

func stripTags(s string) string {
	var b bytes.Buffer
	in := false
	for _, r := range s {
		switch {
		case r == '<':
			in = true
		case r == '>':
			in = false
		case !in:
			b.WriteRune(r)
		}
	}
	return b.String()
}



