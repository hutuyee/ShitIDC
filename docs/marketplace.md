# 插件市场（Marketplace）

ShitIDC 内置插件市场：管理后台 → **扩展与主题 → 插件市场**。市场可一键安装
三类包：

| kind | 说明 | 安装动作 |
|---|---|---|
| `extension` | WASM 扩展（`extension.json` + `.wasm`） | 复用扩展上传流程，落库后手动启用 |
| `theme` | 主题包（CSS 变量契约） | 复用主题安装流程 |
| `zjmf-plugin` | 魔方财务 server 插件（PHP） | 静态解析并转换为 **custom 供应商**（见 [zjmf-plugin-import.md](zjmf-plugin-import.md)） |

## 1. 市场就是一个 JSON 索引

市场没有中心服务器依赖：它只是一个 JSON 文件 + 若干安装包，可以放在任意
静态服务器 / 对象存储 / GitHub 仓库上。后台「市场索引地址」指向你的
`index.json` 即可。

默认索引地址（可用后台设置覆盖）：

```
https://raw.githubusercontent.com/hutuyee/ShitIDC-marketplace/main/index.json
```

### index.json 格式

```json
{
  "version": 1,
  "updated_at": "2026-10-05T00:00:00Z",
  "items": [
    {
      "kind": "extension",
      "name": "demo-logger",
      "version": "1.0.0",
      "title": "事件日志扩展",
      "description": "记录收到的事件并用 kv 计数",
      "author": "you",
      "homepage": "https://example.com/demo-logger",
      "download_url": "https://example.com/pkgs/demo-logger-1.0.0.zip",
      "sha256": "…下载包的 sha256（强烈建议）",
      "permissions": ["log", "storage"],
      "size": 204800
    },
    {
      "kind": "zjmf-plugin",
      "name": "bthosts",
      "version": "1.7.1",
      "title": "宝塔主机对接（魔方插件）",
      "download_url": "https://example.com/pkgs/bthosts.zip",
      "sha256": "…"
    }
  ]
}
```

字段说明：

- `name`：包标识。extension 必须与 `extension.json` 的 `name` 一致；
  zjmf-plugin 应与模块标识（目录名）一致。
- `sha256`：可选但**强烈建议**。安装时与服务端下载内容比对，不一致直接拒绝。
- `download_url`：必须是 http(s)；下载经 SSRF 防护，安装包上限 50 MB。

## 2. 管理 API（`/api/v1/admin` 前缀，需要 `extension.manage` 权限）

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/admin/marketplace` | 拉取索引并标注本机安装状态（`?url=` 可临时覆盖索引地址） |
| POST | `/admin/marketplace/install` | 安装：`{kind, name}` 或直接 `{kind, url, sha256}`；zjmf-plugin 还需 `base_url` 与 `token` |
| GET | `/admin/marketplace/settings` | 读取索引地址设置 |
| PUT | `/admin/marketplace/settings` | 保存索引地址 |

安装请求体示例：

```json
{ "kind": "extension", "name": "demo-logger" }
```

```json
{ "kind": "zjmf-plugin", "name": "bthosts", "base_url": "https://1.2.3.4:8888", "token": "<accesshash>" }
```

## 3. 自托管市场

1. 准备 `index.json`，把安装包放到同一（或任意 CDN）地址；
2. 后台把「市场索引地址」保存为你的 URL；
3. 更新版本时改 `index.json` 的 `version`/`download_url`，后台刷新即可看到
   「可升级」标记（对 extension 按版本号比对）。

安全模型与扩展上传完全一致：manifest 校验、WASM 魔数、Zip Slip 拦截、
权限白名单在安装路径上全部生效，市场只是多了一个"包从哪来"的 URL。
