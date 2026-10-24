package main

// 微信数据库读取（WCDB）
//
// 走腾讯官方 WCDB 引擎解密 biz_message_0.db，读取服务号/公众号消息。
// 这一层覆盖了 hook 抓不到的场景：微信收款助手、微信支付的收款/赞赏通知。

import (
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

// ---------- DLL 绑定 ----------

var (
	wcdbOnce  sync.Once
	wcdbErr   error
	wcdbOpen  *syscall.Proc
	wcdbQuery *syscall.Proc
	wcdbClose *syscall.Proc
	wcdbFree  *syscall.Proc
)

func wcdbInit() error {
	wcdbOnce.Do(func() {
		dir, err := nativeDir()
		if err != nil {
			wcdbErr = err
			return
		}
		// WCDB.dll 是 wxtool_wcdb.dll 的依赖，必须先加载
		if _, err := syscall.LoadDLL(filepath.Join(dir, "WCDB.dll")); err != nil {
			wcdbErr = fmt.Errorf("加载 WCDB.dll 失败: %w", err)
			return
		}
		d, err := syscall.LoadDLL(filepath.Join(dir, "wxtool_wcdb.dll"))
		if err != nil {
			wcdbErr = fmt.Errorf("加载 wxtool_wcdb.dll 失败: %w", err)
			return
		}
		var e error
		if wcdbOpen, e = d.FindProc("wxtool_open"); e != nil {
			wcdbErr = e
			return
		}
		if wcdbQuery, e = d.FindProc("wxtool_query"); e != nil {
			wcdbErr = e
			return
		}
		if wcdbClose, e = d.FindProc("wxtool_close"); e != nil {
			wcdbErr = e
			return
		}
		if wcdbFree, e = d.FindProc("wxtool_free_string"); e != nil {
			wcdbErr = e
			return
		}
	})
	return wcdbErr
}

func cStrGo(p uintptr) string {
	if p == 0 {
		return ""
	}
	n := 0
	for n < 1<<20 {
		if *(*byte)(unsafe.Pointer(p + uintptr(n))) == 0 {
			break
		}
		n++
	}
	if n == 0 {
		return ""
	}
	return string(unsafe.Slice((*byte)(unsafe.Pointer(p)), n))
}

// ---------- 数据库句柄 ----------

// WxDB 一个已解密的微信数据库
// 注意：wxtool 的句柄只能在创建它的 OS 线程上使用，
// 所以创建时用 runtime.LockOSThread 绑定，且所有操作必须在同一 goroutine。
type WxDB struct {
	handle uintptr
	path   string
	mu     sync.Mutex
	closed bool
	reqCh  chan dbReq
}

// OpenWxDB 用密钥打开一个微信数据库
// 打开动作和后续查询全部在同一个锁定线程上完成。
func OpenWxDB(dbPath, keyHex string) (*WxDB, error) {
	if err := wcdbInit(); err != nil {
		return nil, err
	}
	if len(keyHex) != 64 {
		return nil, fmt.Errorf("密钥必须是 64 位十六进制")
	}
	d := &WxDB{path: dbPath, reqCh: make(chan dbReq, 8)}

	type openResp struct {
		handle uintptr
		err    error
	}
	openCh := make(chan openResp, 1)

	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()

		p := append([]byte(dbPath), 0)
		k := append([]byte(keyHex), 0)
		var h, es uintptr
		r, _, _ := wcdbOpen.Call(
			uintptr(unsafe.Pointer(&p[0])),
			uintptr(unsafe.Pointer(&k[0])),
			4096, 0,
			uintptr(unsafe.Pointer(&h)),
			uintptr(unsafe.Pointer(&es)),
		)
		runtime.KeepAlive(p)
		runtime.KeepAlive(k)
		if int32(r) != 0 {
			msg := cStrGo(es)
			if es != 0 {
				wcdbFree.Call(es)
			}
			if msg == "" {
				msg = "打开失败"
			}
			openCh <- openResp{err: fmt.Errorf("%s", msg)}
			return
		}

		// 同一个线程继续处理所有查询
		openCh <- openResp{handle: h}
		for req := range d.reqCh {
			v, err := d.rawQuery(req.op, req.args)
			req.resp <- dbResp{v: v, err: err}
		}
	}()

	r := <-openCh
	if r.err != nil {
		close(d.reqCh)
		return nil, r.err
	}
	d.handle = r.handle
	return d, nil
}

type dbReq struct {
	op   string
	args map[string]any
	resp chan dbResp
}

type dbResp struct {
	v   any
	err error
}

// Close 关闭数据库
func (d *WxDB) Close() {
	if d.closed {
		return
	}
	select {
	case d.reqCh <- dbReq{op: "__close__", resp: make(chan dbResp, 1)}:
	case <-time.After(3 * time.Second):
		return
	}
	close(d.reqCh)
}

// query 执行结构化查询（转发到专用线程）
func (d *WxDB) query(op string, args map[string]any) (any, error) {
	ch := make(chan dbResp, 1)
	select {
	case d.reqCh <- dbReq{op: op, args: args, resp: ch}:
	case <-time.After(10 * time.Second):
		return nil, fmt.Errorf("查询超时")
	}
	r := <-ch
	return r.v, r.err
}

// rawQuery 真正的查询（只在 run 的锁定线程上调用）
func (d *WxDB) rawQuery(op string, args map[string]any) (any, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return nil, fmt.Errorf("数据库已关闭")
	}
	if op == "__close__" {
		wcdbClose.Call(d.handle)
		d.closed = true
		d.handle = 0
		return nil, nil
	}
	if args == nil {
		args = map[string]any{}
	}
	payload, err := json.Marshal(map[string]any{"op": op, "args": args})
	if err != nil {
		return nil, err
	}
	buf := append(payload, 0)
	var out, e2 uintptr
	r, _, _ := wcdbQuery.Call(d.handle,
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(unsafe.Pointer(&out)),
		uintptr(unsafe.Pointer(&e2)))
	runtime.KeepAlive(buf)
	if int32(r) != 0 {
		msg := cStrGo(e2)
		if e2 != 0 {
			wcdbFree.Call(e2)
		}
		if msg == "" {
			msg = "查询失败"
		}
		return nil, fmt.Errorf("%s", msg)
	}
	s := cStrGo(out)
	if out != 0 {
		wcdbFree.Call(out)
	}
	var v any
	if s != "" {
		if err := json.Unmarshal([]byte(s), &v); err != nil {
			return nil, err
		}
	}
	return v, nil
}

// Tables 列出所有表
func (d *WxDB) Tables() ([]string, error) {
	v, err := d.query("tables", nil)
	if err != nil {
		return nil, err
	}
	list, _ := v.([]any)
	out := make([]string, 0, len(list))
	for _, it := range list {
		if m, ok := it.(map[string]any); ok {
			if n, ok := m["name"].(string); ok {
				out = append(out, n)
			}
		}
	}
	return out, nil
}

// Count 某表行数
func (d *WxDB) Count(table string) (int64, error) {
	v, err := d.query("count", map[string]any{"table": table})
	if err != nil {
		return 0, err
	}
	list, _ := v.([]any)
	if len(list) == 0 {
		return 0, nil
	}
	m, _ := list[0].(map[string]any)
	if c, ok := m["count"].(float64); ok {
		return int64(c), nil
	}
	return 0, nil
}

// RawMsg 原始消息行
type RawMsg struct {
	LocalID   int64
	ServerID  int64
	LocalType int64
	SortSeq   int64
	CreateAt  int64
	Content   string // hex 编码（可能是 zstd 压缩）
	IsZstd    bool
}

// Messages 读取消息
func (d *WxDB) Messages(table string, limit int, since int64, ascending bool) ([]RawMsg, error) {
	args := map[string]any{
		"table":     table,
		"limit":     limit,
		"ascending": ascending,
	}
	if since > 0 {
		args["since_seconds"] = since
	}
	v, err := d.query("messages", args)
	if err != nil {
		return nil, err
	}
	list, _ := v.([]any)
	out := make([]RawMsg, 0, len(list))
	for _, it := range list {
		m, _ := it.(map[string]any)
		r := RawMsg{}
		if f, ok := m["local_id"].(float64); ok {
			r.LocalID = int64(f)
		}
		if f, ok := m["server_id"].(float64); ok {
			r.ServerID = int64(f)
		}
		if f, ok := m["local_type"].(float64); ok {
			r.LocalType = int64(f)
		}
		if f, ok := m["sort_seq"].(float64); ok {
			r.SortSeq = int64(f)
		}
		if f, ok := m["create_time"].(float64); ok {
			r.CreateAt = int64(f)
		}
		if s, ok := m["message_content"].(string); ok {
			r.Content = s
			r.IsZstd = strings.HasPrefix(strings.ToLower(s), "28b52ffd")
		}
		if ct, ok := m["WCDB_CT_message_content"].(float64); ok && ct == 4 {
			r.IsZstd = true
		}
		out = append(out, r)
	}
	return out, nil
}

// ---------- 公众号表映射 ----------

// GhTable 公众号 gh_id 对应的消息表名
// 规则：Msg_ + md5(gh_id)
func GhTable(ghID string) string {
	sum := md5.Sum([]byte(ghID))
	return "Msg_" + hex.EncodeToString(sum[:])
}

// ---------- 本地账号发现 ----------

// WxAccount 本地微信账号目录
type WxAccount struct {
	Wxid      string // 从目录名解析
	Dir       string // 完整路径
	BizDB     string // biz_message_0.db 路径
	MsgDB     string // message_0.db 路径
	ContactDB string // contact.db 路径
	HasBiz    bool
	ModTime   time.Time
}

// WeChatRoot 微信数据根目录
func WeChatRoot() string {
	home, _ := os.UserHomeDir()
	for _, p := range []string{
		filepath.Join(home, "Documents", "xwechat_files"),
		filepath.Join(home, "Documents", "WeChat Files"),
	} {
		if st, err := os.Stat(p); err == nil && st.IsDir() {
			return p
		}
	}
	return ""
}

// ScanWxAccounts 扫描本机所有微信账号
func ScanWxAccounts() []WxAccount {
	root := WeChatRoot()
	if root == "" {
		return nil
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	out := make([]WxAccount, 0, 4)
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if name == "all_users" || name == "Backup" {
			continue
		}
		// 目录名形如 wxid_xxx_abcd
		wxid := name
		if i := strings.LastIndex(name, "_"); i > 0 {
			wxid = name[:i]
		}
		if !strings.HasPrefix(wxid, "wxid_") {
			continue
		}
		dir := filepath.Join(root, name)
		acc := WxAccount{Wxid: wxid, Dir: dir}
		acc.BizDB = filepath.Join(dir, "db_storage", "message", "biz_message_0.db")
		acc.MsgDB = filepath.Join(dir, "db_storage", "message", "message_0.db")
		acc.ContactDB = filepath.Join(dir, "db_storage", "contact", "contact.db")
		if st, err := os.Stat(acc.BizDB); err == nil {
			acc.HasBiz = true
			acc.ModTime = st.ModTime()
		}
		out = append(out, acc)
	}
	return out
}

// findWxAccount 按 wxid 找本地账号
func findWxAccount(wxid string) (WxAccount, bool) {
	for _, a := range ScanWxAccounts() {
		if a.Wxid == wxid {
			return a, true
		}
	}
	return WxAccount{}, false
}





