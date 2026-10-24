package main

// 配置

import (
	"golang.org/x/sys/windows/registry"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

// defaultKeywords 默认收款关键词：只上报命中这些词的收款类通知
var defaultKeywords = []string{
	"微信收款助手", "微信支付收款", "二维码赞赏到账", "店员消息",
	"收款单到账", "个人收款码到账", "收款通知", "微信收款商业版",
	"对外收款", "收钱码", "店员通", "已转入余额",
	"你已成功收款", "通过扫码向你付款", "成功收款",
}

type Config struct {
	// 上报模式: "yjk" = 易简控, "v1" = 码支付V1, "none" = 只打印
	Mode string `json:"mode"`
	// 易简控
	YjkSite  string `json:"yjk_site"`
	YjkPid   string `json:"yjk_pid"`
	YjkToken string `json:"yjk_token"`
	// 码支付V1
	V1Site      string `json:"v1_site"`
	V1ChannelID string `json:"v1_channel_id"`
	V1Secret    string `json:"v1_secret"`

	// 过滤
	ForwardAll  bool     `json:"forward_all"`
	Keywords    []string `json:"keywords"`
	IgnoreSelf  bool     `json:"ignore_self"`

	// 行为
	AutoInject  bool `json:"auto_inject"`
	WeChatExe   string `json:"wechat_exe"`

	// 数据库读取（公众号/服务号消息）
	DbEnabled bool        `json:"db_enabled"`  // 是否启用数据库读取
	DbAccounts []DbAccount `json:"db_accounts"` // 多账号列表
	// 旧版单账号字段，启动时自动并入 DbAccounts
	DbWxid string `json:"db_wxid,omitempty"`
	DbKey  string `json:"db_key,omitempty"`
}

// DbAccount 一个需要监控的微信账号
type DbAccount struct {
	Wxid    string `json:"wxid"`
	Key     string `json:"key"`
	Enabled bool   `json:"enabled"`
}

// EffectiveDbAccounts 返回真正要监控的账号列表，含旧字段迁移
func (c Config) EffectiveDbAccounts() []DbAccount {
	out := make([]DbAccount, 0, len(c.DbAccounts)+1)
	seen := map[string]bool{}
	for _, a := range c.DbAccounts {
		if a.Wxid == "" || a.Key == "" || !a.Enabled {
			continue
		}
		if seen[a.Wxid] {
			continue
		}
		seen[a.Wxid] = true
		out = append(out, a)
	}
	// 旧版单账号：并入但不重复
	if c.DbWxid != "" && c.DbKey != "" && !seen[c.DbWxid] {
		out = append(out, DbAccount{Wxid: c.DbWxid, Key: c.DbKey, Enabled: true})
	}
	return out
}

var (
	cfgMu sync.RWMutex
	cfg   = Config{
		Mode:       "none",
		// 默认按关键词转发：只上报收款类通知，避免把普通聊天也推过去
		ForwardAll: false,
		Keywords:   append([]string{}, defaultKeywords...),
		IgnoreSelf: false,
		AutoInject: false,
		WeChatExe:  "",
	}
)


// detectWeChatExe 探测本机微信安装路径（只读注册表和常见目录）
func detectWeChatExe() string {
	home, _ := os.UserHomeDir()
	candidates := []string{
		`D:\Weixin\Weixin.exe`,
		`C:\Program Files\Tencent\Weixin\Weixin.exe`,
		`C:\Program Files (x86)\Tencent\Weixin\Weixin.exe`,
	}
	if home != "" {
		candidates = append(candidates,
			filepath.Join(home, "AppData", "Local", "Tencent", "Weixin", "Weixin.exe"),
		)
	}
	// 注册表卸载项里通常记着安装目录
	if k, err := registry.OpenKey(registry.LOCAL_MACHINE,
		`SOFTWARE\WOW6432Node\Microsoft\Windows\CurrentVersion\Uninstall\Weixin`,
		registry.QUERY_VALUE); err == nil {
		if v, _, e := k.GetStringValue("InstallLocation"); e == nil && v != "" {
			candidates = append([]string{filepath.Join(v, "Weixin.exe")}, candidates...)
		}
		k.Close()
	}
	if k, err := registry.OpenKey(registry.LOCAL_MACHINE,
		`SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\Weixin`,
		registry.QUERY_VALUE); err == nil {
		if v, _, e := k.GetStringValue("InstallLocation"); e == nil && v != "" {
			candidates = append([]string{filepath.Join(v, "Weixin.exe")}, candidates...)
		}
		k.Close()
	}
	for _, p := range candidates {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	return ""
}
func configPath() string {
	exe, _ := os.Executable()
	return filepath.Join(filepath.Dir(exe), "config.json")
}

func LoadConfig() error {
	b, err := os.ReadFile(configPath())
	if err != nil {
		if os.IsNotExist(err) {
			return SaveConfig()
		}
		return err
	}
	cfgMu.Lock()
	defer cfgMu.Unlock()
	return json.Unmarshal(b, &cfg)
}

func SaveConfig() error {
	cfgMu.RLock()
	b, err := json.MarshalIndent(cfg, "", "  ")
	cfgMu.RUnlock()
	if err != nil {
		return err
	}
	return os.WriteFile(configPath(), b, 0644)
}

func GetConfig() Config {
	cfgMu.RLock()
	defer cfgMu.RUnlock()
	c := cfg
	c.Keywords = append([]string{}, cfg.Keywords...)
	return c
}

func SetConfig(c Config) error {
	cfgMu.Lock()
	cfg = c
	cfgMu.Unlock()
	return SaveConfig()
}






