# Extension SDK（WASM 扩展开发指南）

扩展系统（第十/二十四阶段）使用 **WASM (wasip1) 沙箱 + 能力权限 ABI**，不用 Go 原生 `.so` 插件：扩展崩溃、OOM、死循环都不会影响核心进程，能力（权限）逐条强制授权。

## 1. 用 Go 写一个扩展

扩展是一个 Go 程序，用 `//go:wasmimport` 声明宿主能力、`//go:wasmexport` 暴露 ABI 入口。参考实现：[`extensions/demo-logger/main.go`](../extensions/demo-logger/main.go)。

```go
//go:build js || wasip1

package main

import "unsafe"

// 声明宿主能力（只写你要用的）
//go:wasmimport shitidc log
func hostLog(ptr unsafe.Pointer, length uint32)

//go:wasmimport shitidc kv_get
func hostKVGet(keyPtr unsafe.Pointer, keyLen uint32, outPtr unsafe.Pointer, outMax uint32) uint32

//go:wasmimport shitidc kv_set
func hostKVSet(keyPtr unsafe.Pointer, keyLen uint32, valPtr unsafe.Pointer, valLen uint32) uint32

//go:wasmimport shitidc http_fetch
func hostHTTPFetch(urlPtr unsafe.Pointer, urlLen uint32, outPtr unsafe.Pointer, outMax uint32) uint32

//go:wasmimport shitidc random
func hostRandom(outPtr unsafe.Pointer, outLen uint32)

func main() {} // wasip1 入口，保持为空

// ext_alloc：宿主写入数据前向_guest 申请缓冲区
//go:wasmexport ext_alloc
func extAlloc(length uint32) unsafe.Pointer { ... }

// ext_on_event：事件入口，返回 0 表示成功
//go:wasmexport ext_on_event
func extOnEvent(namePtr unsafe.Pointer, nameLen uint32, payloadPtr unsafe.Pointer, payloadLen uint32) int32 {
    logString("saw " + readString(namePtr, nameLen))
    return 0
}
```

## 2. 宿主 ABI v1 速查

宿主模块名 `shitidc`。指针为 `unsafe.Pointer`，长度为 `uint32`；返回 `^uint32(0)`（即 -1）表示失败。

| 导入 | 签名 | 所需权限 |
| --- | --- | --- |
| `log` | `(ptr, len)` | `log` |
| `random` | `(out_ptr, out_len)` | 无（总是授予） |
| `kv_get` | `(key_ptr, key_len, out_ptr, out_max) -> 实际长度` | `storage` |
| `kv_set` | `(key_ptr, key_len, val_ptr, val_len) -> 0/-1` | `storage` |
| `http_fetch` | `(url_ptr, url_len, out_ptr, out_max) -> 实际长度` | `http` |

事件负载为完整事件 JSON（`{"name":..., "occurred_at":..., "data":{...}}`）。kv 存储按扩展名隔离，数据库持久化（`ext_storage` 表）。

## 3. extension.json 清单

```json
{
  "name": "demo-logger",
  "version": "1.0.0",
  "description": "参考扩展：记录收到的核心事件并用 kv 存储计数",
  "entry": "demo-logger.wasm",
  "permissions": ["log", "storage"],
  "events": ["order.paid", "user.login"]
}
```

- `permissions`：只允许 `log` / `storage` / `http`，未申请的能力在宿主 import 层直接拒绝（§23 扩展不能默认拥有所有权限）。
- `events`：订阅的核心事件（Hook 系统事件名），留空 = 订阅全部。

## 4. 构建与安装

```bash
# 构建（Go 1.24+，reactor 模式）
GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -o demo-logger.wasm .
# 或使用脚本
./scripts/build-extension.sh extensions/demo-logger
```

打包 zip（`extension.json` 必须在 zip 根）：

```bash
cd extensions/demo-logger && zip -r ../demo-logger.zip extension.json demo-logger.wasm
```

管理后台 → 扩展 / 主题 → 上传扩展 zip → 启用。上传时校验 manifest、`\0asm` 魔数、Zip Slip 与文件类型；启用后 server 启动自动加载，事件经 Hook 总线分发，扩展错误只记日志、绝不影响业务流。

## 5. 安全边界

- 扩展运行在 wazero 沙箱，无文件系统 / 环境变量 / 原生调用。
- `http_fetch` 强制 HTTPS 且经 SSRF 防护（§42：内网/环回/链路本地地址全部拦截）。
- 单次事件处理 15 秒超时；模块 20MB 上限；kv 单值 1MB 上限。
- 扩展没有余额/支付等敏感核心数据访问能力；需要新能力时应扩展宿主 ABI 并走权限审批，而不是放开沙箱。
