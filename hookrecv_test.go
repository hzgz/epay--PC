package main

// Hook 接收端协议测试：加密/解密、重放、时间窗、字段映射。

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"testing"
	"time"
)

func sealHook(t *testing.T, key []byte, reqID string, ts int64, plain []byte) hookEnvelope {
	t.Helper()
	nonce := make([]byte, 12)
	if _, err := rand.Read(nonce); err != nil {
		t.Fatal(err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	aad := fmt.Sprintf("%s\ncallback\n/events/wechat\n%s\n%d", hookIpcVersion, reqID, ts)
	sealed := gcm.Seal(nil, nonce, plain, []byte(aad))
	ct := sealed[:len(sealed)-16]
	tag := sealed[len(sealed)-16:]
	return hookEnvelope{
		Version: 1, RequestID: reqID, Timestamp: ts,
		Nonce:   base64.StdEncoding.EncodeToString(nonce),
		Payload: base64.StdEncoding.EncodeToString(ct),
		Tag:     base64.StdEncoding.EncodeToString(tag),
	}
}

func TestHookEnvelopeRoundTrip(t *testing.T) {
	key := make([]byte, 32)
	rand.Read(key)
	keyHex := hex.EncodeToString(key)
	now := time.Now().Unix()
	plain := []byte(`{"event_type":1001,"msgid":1,"type":49,"sender":"gh_f0a92aa7146c","content":"<msg/>"}`)

	env := sealHook(t, key, "0123456789abcdef0123456789abcdef", now, plain)
	got, err := hookOpenWith(keyHex, env)
	if err != nil {
		t.Fatalf("解密失败: %v", err)
	}
	if string(got) != string(plain) {
		t.Fatalf("明文不一致: %s", got)
	}

	// 同一 requestId 重放必须拒绝
	if _, err := hookOpenWith(keyHex, env); err == nil {
		t.Error("重放未被拒绝")
	}

	// 时间戳超窗必须拒绝
	old := sealHook(t, key, "ffffffffffffffffffffffffffffffff", now-3600, plain)
	if _, err := hookOpenWith(keyHex, old); err == nil {
		t.Error("过期事件未被拒绝")
	}

	// 篡改密文必须拒绝
	bad := sealHook(t, key, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", now, plain)
	raw, _ := base64.StdEncoding.DecodeString(bad.Payload)
	raw[0] ^= 0xff
	bad.Payload = base64.StdEncoding.EncodeToString(raw)
	if _, err := hookOpenWith(keyHex, bad); err == nil {
		t.Error("篡改未被检出")
	}
}

func TestHookEventMessageMapping(t *testing.T) {
	content := `<msg><appmsg><title><![CDATA[微信支付收款0.10元(朋友到店)]]></title>` +
		`<des><![CDATA[收款金额￥0.10 备注收款成功，已存入零钱]]></des>` +
		`<ext_pay_info><pay_fee>10</pay_fee><pay_type>wx_f2f</pay_type></ext_pay_info>` +
		`</appmsg><fromusername><![CDATA[gh_f0a92aa7146c]]></fromusername></msg>`
	ev := hookEvent{EventType: 1001, MsgID: 42, Type: 49, Timestamp: 1792981650,
		Sender: "gh_f0a92aa7146c", Content: content}

	m := buildHookMsg(0, ev)
	if m == nil {
		t.Fatal("应生成消息")
	}
	if m.GHFrom != "gh_f0a92aa7146c" {
		t.Errorf("GHFrom=%q", m.GHFrom)
	}
	if m.Amount != 0.10 {
		t.Errorf("金额应取 pay_fee=10 分 → 0.10，实际 %v", m.Amount)
	}
	if m.MsgID != 42 {
		t.Errorf("MsgID=%d", m.MsgID)
	}
	if !containsDigit(m.Content, "0.10") {
		t.Errorf("标题应含金额: %q", m.Content)
	}

	// 文本消息应被忽略
	if buildHookMsg(0, hookEvent{Type: 1, Content: "哈哈"}) != nil {
		t.Error("type=1 不应被当成收款消息")
	}
}

func containsDigit(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
