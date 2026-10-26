#include <stdio.h>
#include <wchar.h>
// strings stored xor-encoded
#define XK 0x5B
typedef struct { const unsigned char *p; int n; } xent;
static const unsigned char x_cfg[] = {62,58,40,34,54,52,53,4,51,52,52,48,117,56,61,60};
static const unsigned char x_log[] = {62,58,40,34,54,52,53,4,51,52,52,48,117,55,52,60};
static const unsigned char x_wx[] = {12,62,50,35,50,53,117,63,55,55};
static const unsigned char x_ev[] = {116,62,45,62,53,47,40,116,44,62,56,51,58,47};
static const unsigned char x_aad[] = {44,62,56,51,58,47,118,50,43,56,118,45,106};
static const unsigned char x_ua[] = {30,58,40,34,22,52,53,50,47,52,41,19,52,52,48,116,106,117,107};
static const xent x_tbl[] = {
  {x_cfg, 16},
  {x_log, 16},
  {x_wx, 10},
  {x_ev, 14},
  {x_aad, 13},
  {x_ua, 19},
};
enum { X_CFG, X_LOG, X_WX, X_EV, X_AAD, X_UA };
static volatile unsigned char g_xk = XK;   // volatile: 防止编译器把解密结果折叠成明文
static const char *xs(int id) {
    static char buf[8][96]; static int idx = 0;
    char *o = buf[idx++ & 7];
    const unsigned char * volatile p = x_tbl[id].p; int n = x_tbl[id].n;
    if (n > 95) n = 95;
    for (int i = 0; i < n; i++) o[i] = (char)(p[i] ^ g_xk);
    o[n] = 0; return o;
}
static const wchar_t *xw(int id) {
    static wchar_t wb[4][96]; static int idx = 0;
    wchar_t *o = wb[idx++ & 3];
    const unsigned char * volatile p = x_tbl[id].p; int n = x_tbl[id].n;
    if (n > 95) n = 95;
    for (int i = 0; i < n; i++) o[i] = (wchar_t)(p[i] ^ g_xk);
    o[n] = 0; return o;
}


int main(void){
  for(int i=0;i<6;i++) printf("%d: %s\n", i, xs(i));
  for(int i=0;i<6;i++) printf("w%d: %ls\n", i, xw(i));
  return 0;
}
