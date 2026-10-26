// EasyMonitor Hook DLL（原型阶段 1）
//
// 作用：注入 Weixin.exe，在 printpb（Weixin.dll + 0x67b7d0，4.1.13.63）入口处
// 读取缓冲区，抓取 <msg>…</msg> 的收款消息，原样写入日志文件。
// 本阶段只验证"能抓到"，还没有加密和 HTTP 上报。
//
// 安全策略：
//   1. 只认已经验证过的序言字节，版本不符直接放弃（不写目标内存）；
//   2. 如果入口已被别的 hook 改写成 12 字节绝对跳转，先用磁盘序言恢复，再装我们的；
//   3. 任何一步失败都只写日志退出，不影响微信运行。

#include <windows.h>
#include <stdio.h>
#include <string.h>
#include <stdarg.h>
#include <stdlib.h>
#include <bcrypt.h>
#include <winhttp.h>
#include <time.h>

#define TARGET_RVA 0x67b7D0ULL
#define PATCH_LEN 18 // 覆盖到指令边界：55 56 53 48 81 EC 80 00 00 00 48 8D AC 24 80 00 00 00

static const BYTE kOrig[PATCH_LEN] = {
    0x55, 0x56, 0x53,
    0x48, 0x81, 0xEC, 0x80, 0x00, 0x00, 0x00,
    0x48, 0x8D, 0xAC, 0x24, 0x80, 0x00, 0x00, 0x00,
};

typedef __int64 (*target_fn)(void *, void *, size_t, int);

static target_fn g_tramp = NULL;
static void *g_target = NULL;
static FILE *g_log = NULL;

static void logf_(const char *fmt, ...) {
    if (!g_log) return;
    SYSTEMTIME st;
    GetLocalTime(&st);
    fprintf(g_log, "%02d:%02d:%02d ", st.wHour, st.wMinute, st.wSecond);
    va_list ap;
    va_start(ap, fmt);
    vfprintf(g_log, fmt, ap);
    va_end(ap);
    fputc('\n', g_log);
    fflush(g_log);
}

// 在 [p, p+n) 里找子串
static const char *find_sub(const char *p, size_t n, const char *needle) {
    size_t m = strlen(needle);
    if (m == 0 || n < m) return NULL;
    for (size_t i = 0; i + m <= n; i++) {
        if (memcmp(p + i, needle, m) == 0) return p + i;
    }
    return NULL;
}


// ---------- 配置 / 编码 / 加密 / 上报 ----------

static char g_addr[128] = "127.0.0.1:19100";
static char g_keyhex[128] = "";
static int  g_ready = 0;

static void load_cfg(void) {
    char path[MAX_PATH];
    DWORD n = GetTempPathA(MAX_PATH, path);
    if (!n) return;
    strcat(path, "easymon_hook.cfg");
    FILE *f = fopen(path, "r");
    if (!f) { logf_("没有配置文件 %s", path); return; }
    char line[256];
    while (fgets(line, sizeof(line), f)) {
        char *eq = strchr(line, '=');
        if (!eq) continue;
        *eq = 0;
        char *v = eq + 1;
        while (*v == ' ' || *v == '\t') v++;
        size_t L = strlen(v);
        while (L && (v[L-1] == '\n' || v[L-1] == '\r' || v[L-1] == ' ')) v[--L] = 0;
        if (strcmp(line, "ADDR") == 0) { strncpy(g_addr, v, sizeof(g_addr)-1); }
        else if (strcmp(line, "KEY") == 0) { strncpy(g_keyhex, v, sizeof(g_keyhex)-1); }
    }
    fclose(f);
    g_ready = (strlen(g_keyhex) == 64);
    logf_("配置 addr=%s key=%s", g_addr, g_ready ? "已加载" : "缺失");
}

static int hex2bin(const char *hex, BYTE *out, int max) {
    int n = 0;
    while (hex[0] && hex[1] && n < max) {
        unsigned v; if (sscanf(hex, "%2x", &v) != 1) return -1;
        out[n++] = (BYTE)v; hex += 2;
    }
    return n;
}

static const char B64[] = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";
static char *b64(const BYTE *in, int len, char *out) {
    int i = 0, o = 0;
    while (i + 2 < len) {
        unsigned v = (in[i] << 16) | (in[i+1] << 8) | in[i+2];
        out[o++] = B64[(v >> 18) & 63]; out[o++] = B64[(v >> 12) & 63];
        out[o++] = B64[(v >> 6) & 63];  out[o++] = B64[v & 63];
        i += 3;
    }
    if (i < len) {
        unsigned v = in[i] << 16;
        int rem = len - i;
        if (rem == 2) v |= in[i+1] << 8;
        out[o++] = B64[(v >> 18) & 63]; out[o++] = B64[(v >> 12) & 63];
        out[o++] = rem == 2 ? B64[(v >> 6) & 63] : '=';
        out[o++] = '=';
    }
    out[o] = 0;
    return out;
}

// AES-256-GCM 加密；成功返回 0
static int gcm_encrypt(const BYTE *key, int keyLen,
                       const BYTE *nonce, int nonceLen,
                       const BYTE *aad, int aadLen,
                       const BYTE *plain, int plainLen,
                       BYTE *out, int *outLen, BYTE *tag) {
    BCRYPT_ALG_HANDLE alg = NULL;
    BCRYPT_KEY_HANDLE kh = NULL;
    int rc = -1;
    if (BCryptOpenAlgorithmProvider(&alg, BCRYPT_AES_ALGORITHM, NULL, 0) < 0) return -1;
    if (BCryptSetProperty(alg, BCRYPT_CHAINING_MODE,
                          (PUCHAR)BCRYPT_CHAIN_MODE_GCM,
                          sizeof(BCRYPT_CHAIN_MODE_GCM), 0) < 0) goto done;
    if (BCryptGenerateSymmetricKey(alg, &kh, NULL, 0, (PUCHAR)key, keyLen, 0) < 0) goto done;

    BCRYPT_AUTHENTICATED_CIPHER_MODE_INFO info;
    BCRYPT_INIT_AUTH_MODE_INFO(info);
    info.pbNonce = (PUCHAR)nonce; info.cbNonce = nonceLen;
    info.pbAuthData = (PUCHAR)aad; info.cbAuthData = aadLen;
    info.pbTag = tag; info.cbTag = 16;

    ULONG wrote = 0;
    if (BCryptEncrypt(kh, (PUCHAR)plain, plainLen, &info, NULL, 0,
                      out, plainLen, &wrote, 0) < 0) goto done;
    *outLen = (int)wrote;
    rc = 0;
done:
    if (kh) BCryptDestroyKey(kh);
    if (alg) BCryptCloseAlgorithmProvider(alg, 0);
    return rc;
}

static void http_post(const char *body, int bodyLen) {
    wchar_t waddr[128];
    MultiByteToWideChar(CP_UTF8, 0, g_addr, -1, waddr, 128);
    wchar_t host[128]; int port = 80;
    wchar_t *colon = wcschr(waddr, L':');
    if (colon) { *colon = 0; port = _wtoi(colon + 1); }
    wcscpy(host, waddr);

    HINTERNET s = WinHttpOpen(L"EasyMonitorHook/1.0", WINHTTP_ACCESS_TYPE_NO_PROXY,
                              WINHTTP_NO_PROXY_NAME, WINHTTP_NO_PROXY_BYPASS, 0);
    if (!s) return;
    HINTERNET c = WinHttpConnect(s, host, (INTERNET_PORT)port, 0);
    if (!c) { WinHttpCloseHandle(s); return; }
    wchar_t path[128];
    swprintf(path, 128, L"/events/wechat?pid=%lu", GetCurrentProcessId());
    HINTERNET r = WinHttpOpenRequest(c, L"POST", path, NULL, WINHTTP_NO_REFERER,
                                     WINHTTP_DEFAULT_ACCEPT_TYPES, 0);
    if (r) {
        const wchar_t *hdr = L"Content-Type: application/json\r\n";
        WinHttpSendRequest(r, hdr, (DWORD)-1L, (LPVOID)body, bodyLen, bodyLen, 0);
        WinHttpReceiveResponse(r, NULL);
        DWORD code = 0, sz = sizeof(code);
        WinHttpQueryHeaders(r, WINHTTP_QUERY_STATUS_CODE | WINHTTP_QUERY_FLAG_NUMBER,
                            WINHTTP_HEADER_NAME_BY_INDEX, &code, &sz, WINHTTP_NO_HEADER_INDEX);
        logf_("上报 HTTP %lu (%d 字节)", code, bodyLen);
        WinHttpCloseHandle(r);
    }
    WinHttpCloseHandle(c);
    WinHttpCloseHandle(s);
}

static unsigned long long fnv1a(const char *p, size_t n) {
    unsigned long long h = 1469598103934665603ULL;
    for (size_t i = 0; i < n; i++) { h ^= (unsigned char)p[i]; h *= 1099511628211ULL; }
    return h;
}

static void send_payload(int type, const char *senderIn, const char *content, size_t contentLen) {
    if (!g_ready || contentLen == 0 || contentLen > 200000) return;

    // printpb 对同一条消息会调用两次，2 秒内相同内容只上报一次
    static unsigned long long lastHash = 0;
    static time_t lastAt = 0;
    unsigned long long h0 = fnv1a(content, contentLen);
    time_t now = time(NULL);
    if (h0 == lastHash && now - lastAt <= 2) return;
    lastHash = h0; lastAt = now;

    // 公众号：从 XML 片段里取 <fromusername>（带长度，不越界）
    char sender[128] = "";
    if (senderIn && senderIn[0]) {
        strncpy(sender, senderIn, sizeof(sender) - 1);
    } else if (type == 49) {
        const char *fs = find_sub(content, contentLen, "<fromusername>");
        if (fs) {
            size_t used = (size_t)(content + contentLen - fs);
            fs += strlen("<fromusername>");
            used -= strlen("<fromusername>");
            const char *fe = fs;
            size_t max = used < sizeof(sender) - 1 ? used : sizeof(sender) - 1;
            size_t n = 0;
            if (max > 9 && strncmp(fs, "<![CDATA[", 9) == 0) { fs += 9; max -= 9; }
            while (n < max && fs[n] && fs[n] != '<' && fs[n] != ']') n++;
            memcpy(sender, fs, n);
            sender[n] = 0;
        }
    }

    char *esc = malloc(contentLen * 2 + 64);
    if (!esc) return;
    size_t o = 0;
    for (size_t i = 0; i < contentLen; i++) {
        unsigned char c = (unsigned char)content[i];
        if (c == '"' || c == '\\') { esc[o++] = '\\'; esc[o++] = (char)c; }
        else if (c == '\n') { esc[o++] = '\\'; esc[o++] = 'n'; }
        else if (c == '\r') { esc[o++] = '\\'; esc[o++] = 'r'; }
        else if (c == '\t') { esc[o++] = '\\'; esc[o++] = 't'; }
        else if (c < 0x20) { esc[o++] = ' '; }
        else esc[o++] = (char)c;
    }
    esc[o] = 0;

    unsigned long long msgid = h0;
    char *plain = malloc(o + 512);
    if (!plain) { free(esc); return; }
    int plen = snprintf(plain, o + 512,
        "{\"event_type\":1001,\"msgid\":%llu,\"type\":%d,\"timestamp\":%lu,"
        "\"wxid\":\"%s\",\"sender\":\"%s\",\"roomid\":\"\",\"content\":\"%s\"}",
        msgid, type, (unsigned long)time(NULL), sender, sender, esc);
    free(esc);
    if (plen <= 0) { free(plain); return; }

    BYTE key[32], nonce[12], tag[16];
    if (hex2bin(g_keyhex, key, 32) != 32) { free(plain); return; }
    for (int i = 0; i < 12; i++) nonce[i] = (BYTE)(rand() & 0xff);
    char aad[256];
    unsigned long long reqId = ((unsigned long long)rand() << 32) ^ (unsigned long long)rand()
                               ^ ((unsigned long long)time(NULL) << 20);
    int aadLen = snprintf(aad, sizeof(aad),
        "wechat-ipc-v1\ncallback\n/events/wechat\n%016llx\n%lu",
        reqId, (unsigned long)time(NULL));

    BYTE *ct = malloc(plen + 16);
    int ctLen = 0;
    if (gcm_encrypt(key, 32, nonce, 12, (const BYTE *)aad, aadLen,
                    (const BYTE *)plain, plen, ct, &ctLen, tag) != 0) {
        logf_("加密失败");
        free(plain); free(ct); return;
    }

    char *bCt = malloc((size_t)ctLen * 2 + 8);
    char bNonce[32], bTag[32];
    if (!bCt) { free(plain); free(ct); return; }
    b64(nonce, 12, bNonce);
    b64(ct, ctLen, bCt);
    b64(tag, 16, bTag);
    free(ct);

    char *env = malloc(strlen(bCt) + 512);
    if (env) {
        int n = snprintf(env, strlen(bCt) + 512,
            "{\"version\":1,\"requestId\":\"%016llx\",\"timestamp\":%lu,"
            "\"nonce\":\"%s\",\"payload\":\"%s\",\"tag\":\"%s\"}",
            reqId, (unsigned long)time(NULL), bNonce, bCt, bTag);
        if (n > 0) http_post(env, n);
        free(env);
    }
    free(bCt);
    free(plain);
}

// 抓取：公众号 appmsg（<msg>…</msg>）+ 好友/群聊文字（</msgsource> 之后的可读文本）
static void capture(const char *buf, size_t len) {
    if (!buf || len < 8 || len > 400000) return;

    const char *start = find_sub(buf, len, "<msg>");
    if (!start) start = find_sub(buf, len, "<msg ");
    if (start) {
        size_t used = (size_t)(buf + len - start);
        const char *end = find_sub(start, used, "</msg>");
        if (end) {
            size_t xmlLen = (size_t)(end - start) + 6;
            send_payload(49, "", start, xmlLen);
            return;
        }
    }

    // 好友/群聊文字：</msgsource> 后面是一段 UTF-8 文本（"昵称 : 正文"）
    const char *ms = find_sub(buf, len, "</msgsource>");
    if (!ms) return;
    const char *p = ms + strlen("</msgsource>");
    const char *limit = buf + len;
    const char *best = NULL, *run = NULL;
    size_t bestLen = 0, runLen = 0;
    for (const char *q = p; q < limit; q++) {
        unsigned char c = (unsigned char)*q;
        int printable = (c >= 0x20 && c < 0x7f && c != '`') || c >= 0x80;
        if (printable) {
            if (!run) { run = q; runLen = 0; }
            runLen++;
        } else {
            if (run && runLen > bestLen) { best = run; bestLen = runLen; }
            run = NULL; runLen = 0;
        }
    }
    if (run && runLen > bestLen) { best = run; bestLen = runLen; }
    // 真正的聊天文字是 "昵称 : 正文"；没有这个分隔符的多半是二进制里的可打印片段
    if (best && bestLen >= 4) {
        int hasSep = 0;
        for (size_t i = 0; i + 3 <= bestLen; i++) {
            if (best[i] == ' ' && best[i + 1] == ':' && best[i + 2] == ' ') { hasSep = 1; break; }
        }
        if (hasSep) send_payload(1, "", best, bestLen);
    }
}

static int is_readable(const void *p, size_t len) {
    const BYTE *cur = (const BYTE *)p;
    size_t left = len;
    while (left > 0) {
        MEMORY_BASIC_INFORMATION mbi;
        if (!VirtualQuery(cur, &mbi, sizeof(mbi))) return 0;
        if (mbi.State != MEM_COMMIT) return 0;
        if (mbi.Protect & (PAGE_NOACCESS | PAGE_GUARD)) return 0;
        size_t chunk = (size_t)((const BYTE *)mbi.BaseAddress + mbi.RegionSize - cur);
        if (chunk > left) chunk = left;
        cur += chunk;
        left -= chunk;
    }
    return 1;
}

static __int64 __stdcall hooked(void *a, void *buf, size_t len, int flags) {
    if (buf && len >= 16 && len <= 400000 && is_readable(buf, len)) {
        capture((const char *)buf, len);
    }
    return g_tramp(a, buf, len, flags);
}

// 生成 trampoline：原始 18 字节 + 跳回 target+18
static void *build_trampoline(void *target) {
    void *mem = VirtualAlloc(NULL, 64, MEM_COMMIT | MEM_RESERVE, PAGE_EXECUTE_READWRITE);
    if (!mem) return NULL;
    memcpy(mem, kOrig, PATCH_LEN);
    BYTE *p = (BYTE *)mem + PATCH_LEN;
    // 必须用 12 字节绝对跳转：VirtualAlloc 出来的地址与 Weixin.dll 可能相差数 TB，
    // E9 rel32 只有 ±2GB，会把高 32 位截断后跳飞（曾因此打崩微信）。
    p[0] = 0x48; p[1] = 0xB8;
    *(void **)(p + 2) = (BYTE *)target + PATCH_LEN;
    p[10] = 0xFF; p[11] = 0xE0;
    FlushInstructionCache(GetCurrentProcess(), mem, 64);
    return mem;
}

static BOOL install(void) {
    BYTE *target = NULL;
    // 自测模式：hook 一个固定地址的假目标，用来验证 trampoline 逻辑
    const char *testAddr = getenv("EASYMON_TEST_TARGET");
    if (testAddr && *testAddr) {
        target = (BYTE *)(uintptr_t)strtoull(testAddr, NULL, 16);
        logf_("自测模式 target=%p", target);
    } else {
        HMODULE wx = GetModuleHandleW(L"Weixin.dll");
        if (!wx) {
            logf_("Weixin.dll 未加载");
            return FALSE;
        }
        target = (BYTE *)wx + TARGET_RVA;
    }
    g_target = target;

    BYTE cur[PATCH_LEN];
    memcpy(cur, target, PATCH_LEN);

    int isOrig = memcmp(cur, kOrig, PATCH_LEN) == 0;
    int isReplaced = cur[0] == 0x48 && cur[1] == 0xB8 && cur[10] == 0xFF && cur[11] == 0xE0;

    if (!isOrig && !isReplaced) {
        logf_("序言不匹配，放弃 hook：%02x %02x %02x %02x %02x %02x",
              cur[0], cur[1], cur[2], cur[3], cur[4], cur[5]);
        return FALSE;
    }

    DWORD old = 0;
    if (!VirtualProtect(target, PATCH_LEN, PAGE_EXECUTE_READWRITE, &old)) {
        logf_("VirtualProtect 失败 %lu", GetLastError());
        return FALSE;
    }

    if (!isOrig) {
        // 别人已经 hook 过：先用磁盘序言恢复，避免我们的 trampoline 基于别人的跳转
        memcpy(target, kOrig, PATCH_LEN);
        FlushInstructionCache(GetCurrentProcess(), target, PATCH_LEN);
        logf_("检测到已有 hook，已用原始序言覆盖");
    }

    g_tramp = (target_fn)build_trampoline(target);
    if (!g_tramp) {
        VirtualProtect(target, PATCH_LEN, old, &old);
        logf_("trampoline 分配失败");
        return FALSE;
    }

    BYTE patch[PATCH_LEN];
    patch[0] = 0x48; patch[1] = 0xB8;
    *(void **)(patch + 2) = (void *)hooked;
    patch[10] = 0xFF; patch[11] = 0xE0;
    memset(patch + 12, 0x90, PATCH_LEN - 12);
    memcpy(target, patch, PATCH_LEN);
    FlushInstructionCache(GetCurrentProcess(), target, PATCH_LEN);

    VirtualProtect(target, PATCH_LEN, old, &old);
    logf_("hook 已安装 target=%p tramp=%p", target, g_tramp);
    return TRUE;
}

static DWORD WINAPI init_thread(LPVOID param) {
    (void)param;
    char path[MAX_PATH];
    DWORD n = GetTempPathA(MAX_PATH, path);
    if (n == 0 || n >= MAX_PATH - 32) return 0;
    strcat(path, "easymon_hook.log");
    g_log = fopen(path, "a");
    if (!g_log) return 0;
    logf_("DLL 已加载 pid=%lu", GetCurrentProcessId());
    load_cfg();
    install();
    return 0;
}

static void uninstall(void) {
    if (!g_target) return;
    DWORD old = 0;
    if (VirtualProtect(g_target, PATCH_LEN, PAGE_EXECUTE_READWRITE, &old)) {
        memcpy(g_target, kOrig, PATCH_LEN);
        FlushInstructionCache(GetCurrentProcess(), g_target, PATCH_LEN);
        VirtualProtect(g_target, PATCH_LEN, old, &old);
        logf_("hook 已卸载，序言已恢复");
    }
}

BOOL WINAPI DllMain(HINSTANCE inst, DWORD reason, LPVOID reserved) {
    (void)inst; (void)reserved;
    if (reason == DLL_PROCESS_DETACH && reserved == NULL) {
        uninstall();
    }
    if (reason == DLL_PROCESS_ATTACH) {
        DisableThreadLibraryCalls(inst);
        HANDLE th = CreateThread(NULL, 0, init_thread, NULL, 0, NULL);
        if (th) CloseHandle(th);
    }
    return TRUE;
}
