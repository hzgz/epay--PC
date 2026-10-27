package main

import (
	"os"
	"path/filepath"
	"testing"
)

func isolateAccountState(t *testing.T, path string) {
	t.Helper()
	accMu.Lock()
	oldPath := accPath
	oldList := append([]Account(nil), accList...)
	oldErr, oldLoaded := accLoadErr, accLoaded
	accPath = path
	accList = nil
	accLoadErr = nil
	accLoaded = false
	accMu.Unlock()
	t.Cleanup(func() {
		accMu.Lock()
		accPath = oldPath
		accList = oldList
		accLoadErr = oldErr
		accLoaded = oldLoaded
		accMu.Unlock()
	})
}

func TestMalformedAccountsConfigIsNeverOverwritten(t *testing.T) {
	path := filepath.Join(t.TempDir(), "accounts.json")
	original := []byte("{broken account config")
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	isolateAccountState(t, path)
	accMu.Lock()
	accList = []Account{{Wxid: "preserve-in-memory"}}
	accMu.Unlock()

	if err := loadAccounts(); err == nil {
		t.Fatal("loadAccounts should report malformed JSON")
	}
	if got := accountsCopy(); len(got) != 1 || got[0].Wxid != "preserve-in-memory" {
		t.Fatalf("failed load changed in-memory accounts: %+v", got)
	}
	if err := upsertAccount(Account{Wxid: "wxid_new", Enabled: true}); err == nil {
		t.Fatal("upsertAccount should refuse writes after a failed load")
	}
	if err := ensureAccount("wxid_new"); err == nil {
		t.Fatal("ensureAccount should refuse writes after a failed load")
	}
	if err := saveAccounts(); err == nil {
		t.Fatal("saveAccounts should refuse writes after a failed load")
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(original) {
		t.Fatalf("corrupt config changed: got %q, want original bytes", after)
	}
}

func TestMissingAccountsConfigIsValidEmptyConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "accounts.json")
	isolateAccountState(t, path)
	if err := loadAccounts(); err != nil {
		t.Fatalf("missing config should start as empty: %v", err)
	}
	if err := upsertAccount(Account{Wxid: "wxid_new", Enabled: true}); err != nil {
		t.Fatalf("first account write failed: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("account file not created: %v", err)
	}
}
