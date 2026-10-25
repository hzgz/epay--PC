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

	// X-Ypay（XArrPay 商户版）
	XPaySite      string `json:"xpay_site"`       // 平台地址，例如 https://epay.973700.xyz
	XPayPID       string `json:"xpay_pid"`        // 商户 ID（路径里的 {pid}）
	XPaySecret    string `json:"xpay_secret"`     // 商户密钥，MD5 签名用
	XPayClient    string `json:"xpay_client"`     // 心跳 client_name，默认 易监控PC
	XPayHeartbeat bool   `json:"xpay_heartbeat"`  // 是否发心跳

	// 过滤
	ForwardAll  bool     `json:"forward_all"`
	Keywords    []string `json:"keywords"`
	IgnoreSelf  bool     `json:"ignore_self"`

	// 行为
	AutoInject  bool `json:"auto_inject"`
	WeChatExe   string `json:"wechat_exe"`
	// LogLevel 日志文件级别："off" 不写 / "" 基本(INFO+) / "debug" 详细(含 DEBUG)
	LogLevel string `json:"log_level"`

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
		LogLevel:   "",
		XPaySite:   "https://epay.973700.xyz",
		XPayClient: "易监控PC",
		XPayHeartbeat: true,
	}
)


// detectWeChatExe 探测本机微信安装路径（只读注册表和常见目录）
func detectWeChatExe() string {
	// 最可靠：微信正在运行时，直接问进程要 exe 路径。
	// 换设备、换安装盘符都能命中。
	if p := wechatExeFromProcess(); p != "" {
		return p
	}
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
	if err := json.Unmarshal(b, &cfg); err != nil {
		return err
	}
	decryptConfig(&cfg)
	return nil
}

func SaveConfig() error {
	cfgMu.RLock()
	// 深拷贝：DbAccounts 是切片，直接赋值只复制了切片头，
	// 加密会污染内存里的明文
	c := cfg
	c.Keywords = append([]string{}, cfg.Keywords...)
	if cfg.DbAccounts != nil {
		c.DbAccounts = make([]DbAccount, len(cfg.DbAccounts))
		copy(c.DbAccounts, cfg.DbAccounts)
	}
	cfgMu.RUnlock()

	encryptConfig(&c)

	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return atomicWriteFile(configPath(), b, 0644)
}

// 敏感字段加解密。
// 内存里始终是明文（运行时要用），只在读写文件时转换。
// 老配置是明文，dpapiDecrypt 遇到没有 enc: 前缀的原样返回，自动兼容。

func mustEncrypt(v string) string {
	if v == "" || isEncrypted(v) {
		return v
	}
	e, err := dpapiEncrypt(v)
	if err != nil {
		logWarn("system", "加密配置项失败，将明文保存: %v", err)
		return v
	}
	return e
}

func mustDecrypt(v string) string {
	if v == "" || !isEncrypted(v) {
		return v
	}
	d, err := dpapiDecrypt(v)
	if err != nil {
		logError("system", "解密配置项失败（可能是换了电脑或系统用户），需要重新填写: %v", err)
		return ""
	}
	return d
}

func encryptConfig(c *Config) {
	c.V1Secret = mustEncrypt(c.V1Secret)
	c.XPaySecret = mustEncrypt(c.XPaySecret)
	c.YjkToken = mustEncrypt(c.YjkToken)
	c.DbKey = mustEncrypt(c.DbKey)
	for i := range c.DbAccounts {
		c.DbAccounts[i].Key = mustEncrypt(c.DbAccounts[i].Key)
	}
}

func decryptConfig(c *Config) {
	c.V1Secret = mustDecrypt(c.V1Secret)
	c.XPaySecret = mustDecrypt(c.XPaySecret)
	c.YjkToken = mustDecrypt(c.YjkToken)
	c.DbKey = mustDecrypt(c.DbKey)
	migrated := false
	for i := range c.DbAccounts {
		if isEncrypted(c.DbAccounts[i].Key) {
			c.DbAccounts[i].Key = mustDecrypt(c.DbAccounts[i].Key)
			migrated = true
		}
	}
	_ = migrated
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






