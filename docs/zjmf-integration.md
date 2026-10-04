# 魔方（ZJMF / 智简魔方）对接说明

本文说明 ShitIDC 与 **魔方财务（ZJMF）** 的双向对接：ShitIDC 既能作为下游连上游
魔方，也能作为**上游**被魔方财务当作资源接口使用（这是"魔方商品管理里能选到我们"
的关键）。

---

## 0. 先说清楚：哪些是查证过的，哪些不是

对接魔方最容易踩的坑是**照抄别人的路径**。这里先把证据边界写清楚，避免后续维护
时误以为下面的路径是"魔方官方 API"。

### 0.1 已查证（有源码证据）

魔方财务 3.7.6 的 **服务器模块（server module）** 体系是**明文可读**的，证据来自：

- `public/plugins/servers/bthosts/bthosts.php`（617 行，最简样例）
- `public/plugins/servers/nokvm/nokvm.php`（功能最全）
- `public/plugins/servers/proxmoxve/proxmoxve.php`
- `public/plugins/servers/wlkanglepro/wlkanglepro.php`

可以确证的调用约定：

| 项目 | 结论 | 证据 |
|---|---|---|
| 模块文件位置 | `public/plugins/servers/<标识>/<标识>.php`，丢进去即被识别 | bthosts 目录结构 |
| 函数命名 | `<标识>_<动作>`，如 `bthosts_CreateAccount` | bthosts.php:142 |
| 生命周期函数 | `_TestLink` / `_CreateAccount` / `_SuspendAccount` / `_UnsuspendAccount` / `_TerminateAccount` / `_Renew` / `_ChangePackage` / `_Sync` / `_Status` | bthosts.php:126-617 |
| 成功返回 | 字符串 `'success'`，或 `['status'=>'success','msg'=>...]` | bthosts.php:243, 431 |
| 失败返回 | `['status'=>'error','msg'=>...]`，msg 会显示给用户 | bthosts.php:245 |
| 连接测试返回 | `['status'=>200,'data'=>['server_status'=>1]]`，失败时 `server_status=0` 且带 `msg` | bthosts.php:126-140 |
| 状态轮询返回 | `['status'=>'success','data'=>['status'=>'on/off/waiting/unknown','des'=>'运行中']]` | bthosts.php:435-470 |
| 产品级配置字段 | `<模块>_ConfigOptions()` 返回字段数组（type/name/description/options/default/key） | bthosts.php:10-13 |
| 配置取值 | `$params['configoptions']['<key>']` | bthosts.php:172-193 |
| 接口设置字段 | 名称 / IP / 主机名 / 接口类型 / 分组 / 用户名 / 密码 / 端口 / SSL / Hash(accesshash) / 最大账户数 | 后台 `AddServer~*.js` |
| $params 键 | `hostid` `productid` `domain` `username` `password` `domainstatus` `nextduedate` `server_ip` `server_host` `server_username` `server_password` `accesshash` `secure` `port` `configoptions` `customfields` `user_info` | bthosts.php 全文 + nokvm.php:1525-1545 |
| 写库约定 | 模块自己 `Db::name('host')->update([...])`，密码用 `cmf_encrypt()` | bthosts.php:235-242 |
| 自定义字段 | 模块自建 `customfields` + `customfieldsvalues` 存远程 ID | bthosts.php:223-234 |

**签名算法**（`bthosts.php:14-23` 原文）：

```php
function bthosts_CreateSign($time, $random, $token)
{
    $data['time'] = $time;
    $data['random'] = $random;
    $data['token'] = $token;
    sort($data, SORT_STRING);       // 注意：按“值”而不是“键”排序，且是字符串排序
    $str = implode($data);          // 无分隔符拼接
    $signature = md5($str);
    return strtoupper($signature);  // 大写
}
```

要点：`token` 来自接口设置的 **Hash(accesshash)** 字段，**不随请求发送**，只参与摘要。

### 0.2 未查证（不要当成已知协议）

- 魔方财务**自身的 openapi**（`app/openapi/*`、`data/route/openapi.php`）**全部被
  ionCube 加密**：`app/` 下 355 个 PHP 文件加密率 355/355，路径表、参数名、签名
  算法都无法从源码读出。因此本仓库**不会**声称实现了"魔方 openapi"。
- ShitIDC 里既有的 `/compat/magiccube/v1/login_api`、`/v1/products` 等路径是
  **ShitIDC 自己定义的**（见 `internal/provider/magiccube/client.go`），不是魔方
  官方协议，历史原因沿用了这个名字，不要误认为等同于魔方接口。

> 所以本方案走的是 **0.1 已查证的服务器模块路线**：由 ShitIDC 定义自己的接口
> （`/compat/magiccube/v1/*`），魔方侧的 `shitidc` 模块按上述签名规则调用它。
> 两端都是我们的代码，协议成对演进；魔方升级不会因为我们猜错它的私有 openapi 而失效。

---

## 1. 方向一：ShitIDC 作为下游，连上游魔方财务

在 **管理后台 → 供应商 / 魔方上游** 里：

1. 填供应商名称、类型选「智简魔方财务（上游）」。
2. 填上游魔方财务的**网站地址**、你在上游的**账号**、上游用户中心生成的 **API Key**。
3. 保存 → **测试连接** → **同步产品** → 选择商品 **导入**。

导入后本地商品会自动记录供应商与上游 Product ID，下单支付后由 worker 调用上游开通。

> 上游魔方版本的资源 API 路径可能不同，所以「高级兼容设置」里保留创建/暂停/解除/
> 终止/续费的路径覆盖项。**商品同步开箱即用；自动开通资源需要按你实际上游版本填
> 对动作路径并先测试连接。**

---

## 2. 方向二：ShitIDC 作为上游，让魔方财务卖你的产品 ★

这是「魔方商品管理界面里能选到 ShitIDC」的完整做法。

### 2.1 在 ShitIDC 生成接口密钥

1. 进入 **管理后台 → 供应商 / 魔方上游 → 「作为魔方财务的上游」** 面板。
2. 填接口名称；「归属用户 UID」留空则归属当前管理员（也可以填某个用户的 UID，
   魔方通过这把密钥开通的服务就记在该用户名下并从其余额扣费）。
3. 点 **生成接口密钥**，立刻复制 **Hash**（形如 `zj_xxxxxxxxxxxxxxxx.＜32位密钥＞`）。
   > Secret 只在这一次返回，之后无法再查看；丢了就删掉重新生成。

### 2.2 在魔方财务添加接口

魔方后台 → **接口设置 → 添加接口**：

| 字段 | 填什么 |
|---|---|
| 名称 | 任意，例如 `ShitIDC 主站` |
| 接口类型 | **ShitIDC**（安装本模块后才会出现） |
| 接口地址 / IP | ShitIDC 的访问地址，例如 `https://idc.example.com`（**不用带路径**） |
| 端口 | 留空（若地址里已含 `https://` 会自动忽略端口） |
| SSL | 如果 ShitIDC 是 HTTPS，这里勾上；若地址已写 `https://` 也会自动用 HTTPS |
| Hash | 粘贴 2.1 生成的 `Hash` |
| 用户名 / 密码 | 本模块不使用，可留空 |
| 接口分组 | 新建一个分组，把这条接口放进去 |

保存后点 **测试连接**：模块会请求 `POST /compat/magiccube/v1/test`。
成功时提示「连接正常，可售商品 N 个」。

### 2.3 在魔方商品管理里绑定

**商品管理 → 新建/编辑商品**：

1. **接口类型** 选「通用」（本地接口，即走服务器模块）——**不要**选「财务API」，
   财务 API 是魔方自己那套加密 openapi，与本文无关。
2. **接口分组** 选 2.2 里创建的分组。
   > **关键**：魔方核心里 `shd_products.api_type` 必须是 `''`（空）或 `normal` 才会走
   > 服务器模块；如果选成 `zjmf_api`（财务API），核心会跳过模块直接去调它自己那套加密
   > openapi，本模块永远不会被调用（证据：`nokvm.php:1540` 的定时任务过滤条件
   > `whereIn('b.api_type',['','normal'])`）。
   >
   > 绑定链是：`server_groups.type` = 模块标识（`shitidc`）→ `servers.gid` 指向该组 →
   > `products.server_group` 指向该组 → `products.server_type` 存模块类型串。
   > 本模块的字段最终落在 `products.config_option1..24`，按 `_ConfigOptions()` 的下标顺序对应。

3. 在 **模块设置** 里配置本模块的三个字段：

   | 字段 | 说明 |
   |---|---|
   | ShitIDC 商品 ID | 在 ShitIDC 商品管理里复制的商品 UUID |
   | 或从列表中选商品 | 打开商品页时自动从 ShitIDC 拉取的商品下拉（需接口地址与 Hash 已保存） |
   | 计费周期 | `monthly` / `quarterly` / `semiannually` / `yearly`，要与 ShitIDC 该商品的价格档一致 |

4. 设置价格、开通方式（推荐「付款后自动开通」）后保存。

### 2.4 安装模块

把 `zjmf-plugin/shitidc` 整个目录放到魔方财务的 `public/plugins/servers/` 下：

```
public/plugins/servers/shitidc/
├── shitidc.php                 ← 必须有，函数名以 shitidc_ 开头
├── version.txt
└── templates/
    └── information.html        ← 客户中心「主机信息」页签
```

也可以直接用打包好的 zip：`zjmf-plugin/shitidc-module.zip`（解压后放到上面位置）。

`version.txt` 里是**裸版本号**（无换行），与 `_MetaData()['APIVersion']` 一致，参考模块
`bthosts` 的 `version.txt` 也是这个格式。

放好后刷新后台，**接口设置 → 添加接口 → 接口类型** 下拉里就会出现 **ShitIDC**。

验证模块被识别（可选）：魔方后台的接口设置页会请求 `provision/<server_group_id>`
取模块配置项，添加接口时下拉数据来自 `get_modules_group`，测试连接按钮打到
`server_test_link/<id>`——若这三处都能看到 ShitIDC 且测试连接返回正常，说明模块加载成功。

### 2.5 完整链路

```
魔方客户下单并付款
        │  魔方核心按 auto_setup 触发开通
        ▼
shitidc_CreateAccount($params)
        │  POST /compat/magiccube/v1/host   act=create
        │  time + random + signature(token=Hash)
        ▼
ShitIDC  /compat/magiccube/v1/host
        │  1. 验签（api_sign，5 分钟防重放窗口）
        │  2. 校验接口密钥权限与归属用户
        │  3. 下单（服务端重算价格）→ 余额支付 → 入队开通
        │  4. Idempotency-Key = magiccube:<key_id>:<hostid> 防重复开通
        ▼
返回 {"code":1,"msg":"success","data":{"id":"<服务UUID>","username":..,"password":..}}
        │
        ▼
魔方写入 customfields(shitidc_host_id) 与 host 表，之后暂停/删除都靠这个 ID
```

暂停 / 解除暂停 / 删除 / 续费走同一入口的 `act=locked|unlocked|recycle|renew`，
复用 ShitIDC 既有的抢占式状态机（`ClaimServiceForTransition`），因此和管理后台按钮
并发操作时只有一个生效，失败会自动回滚状态并记录原因。

---

## 3. 接口清单（ShitIDC 侧）

所有请求：`POST`，`application/x-www-form-urlencoded`，响应统一
`{"code":1,"msg":"...","data":{...}}`（`code=1` 成功，否则 `msg` 即错误原因）。

公共鉴权参数：

| 参数 | 说明 |
|---|---|
| `time` | Unix 秒 |
| `random` | 随机整数 |
| `signature` | `strtoupper(md5(sort_string(time, random, token)))` |
| `token` | `<key_id>.<secret>`，即接口设置里的 Hash |

| 用途 | 路径 | 额外参数 |
|---|---|---|
| 连接测试 | `/compat/magiccube/v1/test` | — |
| 商品列表 | `/compat/magiccube/v1/product` | — |
| 开通 | `/compat/magiccube/v1/host` | `act=create`、`product_id`、`billing_cycle`、`username`、`password`、`domain`、`nextduedate` |
| 查询 | `/compat/magiccube/v1/host` | `act=status`、`id` |
| 同步 | `/compat/magiccube/v1/host` | `act=sync`、`id` |
| 暂停 | `/compat/magiccube/v1/host` | `act=locked`、`id` |
| 解除暂停 | `/compat/magiccube/v1/host` | `act=unlocked`、`id` |
| 删除 | `/compat/magiccube/v1/host` | `act=recycle`、`id` |
| 续费 | `/compat/magiccube/v1/host` | `act=renew`、`id` |

也提供魔方模块风格的独立路径（等价于上面的 act 形式）：
`/host/create`、`/host/status`、`/host/sync`、`/host/locked`、`/host/start`、`/host/recycle`、`/host/renew`。

### 3.1 计费语义

`create` 与 `renew` **会真实扣费**：在接口密钥归属用户名下创建订单并立刻用钱包余额
结算。余额不足时返回 `HTTP 402` 与中文说明，魔方会把任务标记失败并显示原因——这是
刻意的设计，避免下游凭空开通。给接口密钥指定一个专门的「上游客户」用户并预充值，
是最省心的用法。

### 3.2 安全

- 密钥以 `MASTER_KEY_BASE64` 做 AES-256-GCM 加密存储（表 `upstream_keys`），
  因为验签必须拿到原文；用户级 `api_tokens` 只存 HMAC，无法用于此用途。
- 签名带时间戳，默认 **5 分钟** 容差，重放窗口有限。
- 一把密钥只能操作**它自己开通的**服务：暂停/删除/续费前校验服务归属用户。
- 后台可随时停用（`active=false`）或删除密钥，立即失效。

---

## 4. 排错

| 现象 | 原因与处理 |
|---|---|
| 测试连接报「缺少接口密钥」 | 接口设置的 **Hash** 字段为空，把 ShitIDC 生成的 Hash 填进去 |
| 报「签名校验失败：invalid signature」 | Hash 复制不全/多了空格；或密钥已被删除后重新生成（旧 Hash 立即失效） |
| 报「请求时间戳超出容差」 | 魔方与 ShitIDC 服务器时间相差超过 5 分钟，请校时（NTP） |
| 报「接口密钥无效或已停用」 | 密钥被停用/删除，或归属用户被禁用 |
| 报「上游余额不足」 | 给密钥归属用户在 ShitIDC 充值；订单已创建，充值后可重试 |
| 报「商品不存在或不可订购」 | 商品 UUID 填错，或该商品/价格档已下架 |
| 报「主机当前状态不允许该操作」 | ShitIDC 侧该服务已暂停/终止，属正常幂等保护 |
| 接口类型下拉里没有 ShitIDC | 模块目录没放对，确认路径是 `public/plugins/servers/shitidc/shitidc.php` 且首行是 `<?php` |
| 商品下拉是空的 | 打开商品页时无法定位接口参数属于正常回退，直接在「ShitIDC 商品 ID」里粘 UUID 即可 |

后台的接口密钥列表会显示 **最近调用时间** 与 **最近错误**，排错时先看这一列。

---

## 5. 模块契约的完整证据

本模块用到的每一个魔方侧约定（文件布局、函数签名、返回值格式、`$params` 键名、
写库字段、ConfigOptions 声明、后台绑定链、签名算法）都在下面这份手册里逐条给出
**源码文件:行号 + 原文片段**：

- [docs/zjmf-server-module-contract.md](zjmf-server-module-contract.md)（1443 行，移植手册）

其中明确标注了 **10 处"未找到明文证据"** 的地方（例如 `version.txt` 由谁读取、
`_Renew` 缺失时核心如何表现、`active_logs()`/`aesPasswordDecode()` 的实现），
因为这些函数定义在 ionCube 加密的核心文件里。遇到魔方升级或行为异常时，先查这份手册
确认某个约定是"已确证"还是"推断"。

### 5.1 已知的魔方侧坑（手册第 8 节）

- `proxmoxve.php` 的 `_TestLink` 返回裸字符串 `'ok'`/`'bad'`，与后台
  `ServerSettings` 读取的 `r.data.data.server_status` 结构不兼容 —— **不要照抄**。
  本模块返回 `['status'=>200,'data'=>['server_status'=>1,...]]`。
- `proxmoxve.php` 的 `_TerminateAccount` 写 `domainstatus='Terminated'`，但
  `shd_host.domainstatus` 的 enum 里没有这个值（只有
  Pending/Active/Suspended/Cancelled/Fraud/Completed/Deleted）—— **不要照抄**。
  本模块只写 `Active`。
- `_Status` 的 status 词各模块不统一（`on/off/process/suspend/unknown` 与
  `on/off/waiting/unknown` 都出现过）。本模块与 ShitIDC 约定用
  `on/off/waiting/unknown`，并由 `internal/api/api_magiccube_test.go` 的
  `TestUpstreamStatusVocabulary` 锁死，防止两端漂移。

## 6. 与魔方财务的功能差异

见 [docs/zjmf-diff.md](zjmf-diff.md)：商品/定价/开通模块体系的逐项对照，
以及仍待补齐的差距（配置项、自定义字段、库存限购等）。
