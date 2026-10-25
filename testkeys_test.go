package main

// 测试用的数据库密钥从环境变量读，避免把真实密钥写进仓库。
//
//   set WX_TEST_KEY1=<账号1的32字节hex>
//   set WX_TEST_KEY2=<账号2的32字节hex>
//
// 没设置时相关测试会自动 Skip。

import "os"

func testKey1() string { return os.Getenv("WX_TEST_KEY1") }
func testKey2() string { return os.Getenv("WX_TEST_KEY2") }
