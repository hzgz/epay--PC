package main

// 配置

import (
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
}

var (
	cfgMu sync.RWMutex
	cfg   = Config{
		Mode:       "none",
		// 默认按关键词转发：只上报收款类通知，避免把普通聊天也推过去
		ForwardAll: false,
		Keywords:   append([]string{}, defaultKeywords...),
		IgnoreSelf: false,
		AutoInject: true,
		WeChatExe:  `D:\Weixin\Weixin.exe`,
	}
)

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


