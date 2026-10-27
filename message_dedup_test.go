package main

import "testing"

func TestMessageDedupUsesSourceIDs(t *testing.T) {
	base := ChatMsg{
		From: "gh_pay", To: "wxid_account", Content: "收款 1.00 元", Time: 1792981650,
	}

	one := base
	one.LocalID = 101
	two := base
	two.LocalID = 102
	if messageDedupKey(&one) == messageDedupKey(&two) {
		t.Fatal("different local IDs with identical content/time must remain distinct")
	}
	duplicate := one
	if messageDedupKey(&one) != messageDedupKey(&duplicate) {
		t.Fatal("the same local ID must deduplicate")
	}
	databaseCopy := one
	databaseCopy.StableID = "wechat-server:5001"
	databaseCopy.LocalID = 101
	hookCopy := one
	hookCopy.StableID = "wechat-server:5001"
	hookCopy.LocalID = 0
	if messageDedupKey(&databaseCopy) != messageDedupKey(&hookCopy) {
		t.Fatal("the same server ID from two collectors must deduplicate")
	}

	one.LocalID = 0
	one.StableID = "wechat-hook:9001"
	two.LocalID = 0
	two.StableID = "wechat-hook:9002"
	if messageDedupKey(&one) == messageDedupKey(&two) {
		t.Fatal("different source IDs with identical content/time must remain distinct")
	}
}

func TestExtractMessageServerIDPrefersNestedPath(t *testing.T) {
	fields := []PBField{
		{Path: "2.2.2.2.1", Wire: 0, Var: 7},
		{Path: "2.2.2.2.2.1", Wire: 0, Var: 5001},
	}
	if got := extractMessageServerID(fields); got != 5001 {
		t.Fatalf("nested server ID=%d, want 5001", got)
	}
	if got := stableWeChatMsgID(5001); got != "wechat-server:5001" {
		t.Fatalf("stable ID=%q", got)
	}
}

func TestRecordIDUsesSourceIDs(t *testing.T) {
	base := MsgView{From: "gh_pay", To: "wxid_account", Content: "收款 1.00 元", Time: 1792981650}
	one := base
	one.LocalID = 101
	two := base
	two.LocalID = 102
	if msgID(&one) == msgID(&two) {
		t.Fatal("different local IDs must produce distinct record IDs")
	}
	if msgID(&one) != msgID(&one) {
		t.Fatal("record IDs must be stable")
	}
}

func TestDeleteRecordsUsesCanonicalStableID(t *testing.T) {
	item := MsgView{
		ID: "legacy-fingerprint", From: "gh_pay", To: "wxid_account",
		Content: "收款 1.00 元", Time: 1792981650, LocalID: 101,
	}
	e := &Engine{history: []MsgView{item}}
	oldDirty := recDirty
	defer func() { recDirty = oldDirty }()
	if n := e.deleteRecords(map[string]bool{msgID(&item): true}); n != 1 {
		t.Fatalf("deleted %d records, want 1", n)
	}
}
