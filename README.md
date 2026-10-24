# 易监控PC版

微信收款消息监听工具（Windows）。读取微信 PC 端的公众号收款通知，转发到你自己的支付平台。

**不修改微信进程、不注入、不 hook**，只以只读方式打开微信的 `biz_message_0.db` 读取新消息。

## 功能

- 读取微信 PC 端的收款通知（微信收款助手、微信支付、赞赏、店员消息）
- 支持两种接口：X-Ypay（易简控）、码支付 V1
- 名单外公众号自动降级为手机合同，兼容微信收款商业版
- 按关键词过滤（默认 15 个收款关键词）
- 收款记录持久化，重启不丢，可直接对账
- 回调状态、回调报文、回调 URL 完整留存
- 系统托盘、关闭确认

## 环境要求

- Windows 10/11 x64
- 微信 PC 版 4.1.x（已测 4.1.13.63）
- WebView2 运行时（Win10/11 通常自带）

## 使用

1. 启动微信并登录
2. 运行 `EasyMonitorPC.exe`（单文件，无需安装）
3. 「微信账号」→「添加微信账号」→ 选本地账号 → 填通行密钥
4. 「通知设置」里配置接口模式与平台参数
5. 收款时自动上报

首次运行会在 exe 同目录生成 `config.json`（默认配置），微信安装路径自动识别。

## 关于通行密钥

读取微信数据库需要该账号的通行密钥（64 位十六进制）。有两种获取方式：

- **手动填写**：从已有工具或记录中取得
- **自动获取**：在「关于」页对作者名连续点击 6 次开启开发者模式，「添加微信账号」弹窗会出现「自动获取」按钮。该功能需要微信在挂载 hook 后重新登录一次才可捕获密钥。

## 构建

三个 DLL（`WCDB.dll`、`wxtool_wcdb.dll`、`wx_key.dll`）已放在 `native/`，编译时通过 `go:embed` 打进 exe，产物是单文件。

```bash
go build -ldflags="-H windowsgui -s -w" -o EasyMonitorPC.exe .
```

改了 `ui/index.html` 后建议加 `-a` 强制重编译，避免 embed 缓存没更新：

```bash
go build -a -ldflags="-H windowsgui -s -w" -o EasyMonitorPC.exe .
```

图标与版本信息在 `rsrc_windows_amd64.syso`（由 `app.rc` + `windres` 生成）。

## 目录

```
main.go        程序入口、hook 引擎
wxdb.go        WCDB 解密与查询（线程锁定）
wxmsg.go       zstd 解压、XML 解析、金额提取
wxmon.go       数据库轮询与上报调度
wxkey.go       通行密钥自动提取
report.go      两种上报协议
server.go      内嵌 HTTP 服务与 API
ui/index.html  界面（go:embed）
native/        三个运行期 DLL
```

## 版本

v1.11
