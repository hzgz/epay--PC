package main

// 账号通道绑定：每个微信账号（wxid）可以单独配置通道值

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

type Account struct {
	Wxid       string `json:"wxid"`        // 微信账号（wxid_xxx）
	Nick       string `json:"nick"`        // 备注名，可手动填
	ChannelID  string `json:"channel_id"`  // 通道 ID（码支付 V1 用）
	Secret     string `json:"secret"`      // 商户密钥 / token（可选，空则用全局）
	MerchantID string `json:"merchant_id"` // 商户号 / PID（可选）
	Enabled    bool   `json:"enabled"`     // 是否启用该账号的上报
	Note       string `json:"note"`
}

var (
	accMu   sync.Mutex
	accList []Account
	accPath string
)

func accountsFile() string {
	if accPath != "" {
		return accPath
	}
	exe, err := os.Executable()
	dir := "."
	if err == nil {
		dir = filepath.Dir(exe)
	}
	accPath = filepath.Join(dir, "accounts.json")
	return accPath
}

func loadAccounts() {
	accMu.Lock()
	defer accMu.Unlock()
	b, err := os.ReadFile(accountsFile())
	if err != nil {
		accList = []Account{}
		return
	}
	var v []Account
	if json.Unmarshal(b, &v) == nil {
		accList = v
	}
}

func saveAccounts() error {
	accMu.Lock()
	defer accMu.Unlock()
	b, err := json.MarshalIndent(accList, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(accountsFile(), b, 0644)
}

func accountsCopy() []Account {
	accMu.Lock()
	defer accMu.Unlock()
	out := make([]Account, len(accList))
	copy(out, accList)
	return out
}

// findAccount 按 wxid 查账号配置
func findAccount(wxid string) (Account, bool) {
	accMu.Lock()
	defer accMu.Unlock()
	for _, a := range accList {
		if a.Wxid == wxid {
			return a, true
		}
	}
	return Account{}, false
}

// upsertAccount 新增或更新一个账号
func upsertAccount(a Account) {
	accMu.Lock()
	for i := range accList {
		if accList[i].Wxid == a.Wxid {
			accList[i] = a
			accMu.Unlock()
			saveAccounts()
			return
		}
	}
	accList = append(accList, a)
	accMu.Unlock()
	saveAccounts()
}

func removeAccount(wxid string) {
	accMu.Lock()
	out := make([]Account, 0, len(accList))
	for _, a := range accList {
		if a.Wxid != wxid {
			out = append(out, a)
		}
	}
	accList = out
	accMu.Unlock()
	saveAccounts()
}

// deleteAccountAll 删除一个账号的全部配置：账号记录、通道绑定、数据库监控、密钥。
// 订单记录（records.json）和支付信息一律保留，方便对账。
func deleteAccountAll(wxid string) {
	removeAccount(wxid)

	// 停掉该账号的数据库监控（不影响其他账号）
	StopWxMonitorFor(wxid)
	logInfo("config", "已停止账号 %s 的数据库监控", wxid)

	// 从多账号配置里移除该账号
	c := GetConfig()
	cfg := c
	kept := make([]DbAccount, 0, len(cfg.DbAccounts))
	changed := false
	for _, a := range cfg.DbAccounts {
		if a.Wxid == wxid {
			changed = true
			continue
		}
		kept = append(kept, a)
	}
	cfg.DbAccounts = kept
	// 旧版单账号字段指向它时一并清掉
	if cfg.DbWxid == wxid {
		cfg.DbWxid = ""
		cfg.DbKey = ""
		changed = true
	}
	if changed {
		SetConfig(cfg)
		logInfo("config", "已清除账号 %s 的数据库配置", wxid)
	}

	logInfo("config", "账号已删除 %s（订单记录与支付信息保留）", wxid)
}

// ensureAccount 从消息里发现新账号时自动建档
func ensureAccount(wxid string) {
	if wxid == "" {
		return
	}
	accMu.Lock()
	for _, a := range accList {
		if a.Wxid == wxid {
			accMu.Unlock()
			return
		}
	}
	accList = append(accList, Account{Wxid: wxid, Enabled: true})
	accMu.Unlock()
	saveAccounts()
}


