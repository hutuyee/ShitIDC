# ShitIDC 与魔方财务（ZJMF 3.7.6）差异分析与对接结论

对照对象：魔方财务 V10 / 3.7.6（`_zjmf_ref/zjmf-finance`，ThinkPHP 5 + MySQL，表前缀 `shd_`）
对照主体：ShitIDC（Go + Gin + PostgreSQL + Vue 3）

> **证据说明**：魔方财务的 `app/` 目录 355 个 PHP 文件**全部经 ionCube 加密**（355/355），
> 控制器、模型、路由都无法直接阅读。本报告的魔方侧结论全部来自**可读的旁证**：
> `public/install/thinkcmf.sql`（全量建表）、`public/upgrade/*.sql`（增量字段）、
> `downloads/database/auth_rule.sql`（控制器/方法映射）、`public/admin/js/*`（后台是 Vue
> 编译产物）、`public/admin/lang/zh.js`（字段中文名）、`public/plugins/servers/*`（明文
> 服务器模块）、`public/themes/**`（前台模板）。凡属推断的地方都单独标注。

---

## 1. 技术栈与架构

| 维度 | 魔方财务 3.7.6 | ShitIDC |
|---|---|---|
| 语言/框架 | PHP + ThinkPHP 5.x | Go + Gin |
| 数据库 | MySQL（表前缀 `shd_`，WHMCS 式表名） | PostgreSQL（UUID 公开 ID + 自增内部 ID） |
| 后台前端 | Vue 2 SPA（编译产物，加密业务逻辑在 PHP 侧） | Vue 3 + TypeScript 源码 |
| 部署 | 单机 PHP-FPM + ionCube 扩展 + 授权码 | Docker Compose 分层：API / Worker / Scheduler |
| 异步 | `shd_run_maping` 队列表 + `app/queue/job` | Redis + Asynq，任务可重试/补偿 |
| 授权 | **需要 idcsmart.so 扩展与授权码**，核心代码 ionCube 加密 | 全部开源源码，无加密 |
| 可读性 | 核心业务不可读，二次开发依赖插件钩子 | 全栈可读可改 |

**结论**：架构差异是根本性的。魔方是"加密内核 + 插件扩展"，ShitIDC 是"全开源可改"。
魔方的插件生态（server / gateway / addon / oauth / sms / captcha / certification 七类）
比 ShitIDC 的 WASM 扩展 + Provider 抽象更成熟，但 ShitIDC 能直接改核心。

---

## 2. 功能对照

### 2.1 商品与定价

| 能力 | 魔方财务 3.7.6 | ShitIDC 现状 | 差异 |
|---|---|---|---|
| 商品主档 | `shd_products`（name/description/gid/type/hidden/retired/is_featured/order） | `products`（name/description/group_id/sort_weight/active） | ShitIDC 缺 `type`（产品类型分类）、特色推荐、下架与隐藏分离 |
| 描述 | `description` varchar(1000)，前台 `<pre style="white-space:pre-wrap">` 纯文本渲染 | `description` TEXT，前台原样展示（本次已改为管理员手写直出） | **已对齐**：都是纯文本、都无 markdown/富文本编辑器 |
| 计费周期 | **13 种固定周期**（一次/小时/天/试用/月/季/半年/年/2~10 年），`shd_pricing` 每商品×币种一行，初装费分周期 | 月/季/半年/年 4 种（`product_prices` 多行） | **ShitIDC 明显偏少**：无按小时/按天/一次性/试用，无初装费 |
| 付款类型 | `pay_type`：free/onetime/recurring/day/hour/ontrial，可多值组合 | 无此概念（价格档即周期） | **ShitIDC 缺免费商品、试用、一次性商品** |
| 预付/后付 | `pay_method`：prepayment / postpaid | 仅预付费 | **ShitIDC 缺后付费** |
| 库存 | `stock_control` + `qty`，单客户最大购买数，一次可买多件 | 无库存控制（`stock: -1` 恒为不限） | **ShitIDC 缺库存与限购** |
| 按量/超量计费 | 磁盘/带宽超支单价（`overages_*`）、DCIM 流量包体系 | 无 | **ShitIDC 缺按量计费** |
| 多币种 | `shd_currencies` + 每币种独立定价 | `currencies` 表 + 汇率，基础货币 CNY，**价格档未按币种分行** | ShitIDC 有表但未做到"每币种独立定价" |
| 客户组差异定价 | `shd_user_product_bates`（比例/固定/优惠，按客户组×产品组） | 用户组整体折扣（`discount_percent`） | ShitIDC 只做到组级统一折扣，缺按产品的差异定价 |
| 配置项 | `shd_product_config_options`（下拉/单选/是否/数量 + 阶梯价 + 条件联动） | 无 | **ShitIDC 完全没有可配置选项** |
| 自定义字段 | `shd_customfields`（product/client/ticket 多态） | 无 | **ShitIDC 没有自定义字段** |
| 升降级 | `shd_upgrades` + `shd_product_upgrade_products`，按剩余天数折算差价，模块回调 `_ChangePackage` | 无 | **ShitIDC 缺升降级** |
| 自动删除天数 | `auto_terminate_days`（可当试用期用） | `AUTO_TERMINATE_SUSPENDED_DAYS`（仅针对已暂停服务） | 语义不同，ShitIDC 无"开通 N 天后删除" |

### 2.2 订单与支付

| 能力 | 魔方财务 3.7.6 | ShitIDC |
|---|---|---|
| 订单链 | `shd_orders` → `shd_invoices` + `shd_invoice_items` → `shd_pay_log` | `orders` → `invoices` + `invoice_items` → `payments`（结构基本同构） |
| 购物车 | `shd_cart_session` + 多步结算（configureproduct → viewcart → checkout） | 无购物车，商品卡直接下单弹窗 |
| 支付网关 | 插件式，数量多（微信/支付宝/易支付/PayPal/虎皮椒/自定义） | 注册表式：易支付 + Stripe + 支付宝官方 |
| 退款 | 有 | 钱包冲正 + 网关原路退回，自动分发 |
| 优惠码 | `shd_promo_code` | `coupons`（fixed/percent，事务内原子核销、商品范围、每人限用） |
| 推广返佣 | 有（`affiliate_*`） | 有（邀请码 + 支付后按比例入余额，幂等） |
| 工单 | 有（含评分版插件） | 有（附件、双向回复、邮件通知） |

### 2.3 开通与模块体系 —— 最核心的差异

| 能力 | 魔方财务 3.7.6 | ShitIDC |
|---|---|---|
| 打通上游的方式 | **服务器模块插件**：`public/plugins/servers/<标识>/<标识>.php`，函数式 `_CreateAccount` 等约定 | **Provider 接口**：`internal/provider`，magiccube / proxmox / virtualizor 三个实现 |
| 服务器（接口）管理 | `shd_servers` + `shd_server_groups`（接口分组，平均分配/满一个算一个） | `providers` 表（无分组、无容量分配） |
| 商品绑定模块 | 商品上选 `server_type`（模块标识）+ `server_group`（接口分组） | 商品上选 `provider_id` |
| 模块级产品配置 | `_ConfigOptions()` 在商品页渲染字段，运行时 `$params['configoptions']` | 无（商品只存 provider_product_ref） |
| 主机状态机 | Pending/Active/Suspended/Cancelled/Fraud/Completed/Deleted（+3.0.3 增三种） | pending/provisioning/active/suspending/unsuspending/suspended/terminating/terminated/failed |
| 生命周期动作 | 模块内自实现，核心只做调度 | Provider 接口方法，worker 统一分发 |
| 失败重试 | `shd_module_queue`（num_retries/completed）+ `shd_run_maping`（from_type 400/500） | Asynq 重试 + Scheduler 补偿过渡态 |
| 上游代理商品 | `api_type='zjmf_api'` 的商品**跳过 server 模块直接走上游 openapi**，有 `upstream_*` 一整套字段 | magiccube provider：同步商品 + 导入，动作路径需手工配置 |

**结论**：魔方的"接口分组 + 模块产品配置字段"是 ShitIDC 没有的能力。本次已通过
**魔方 server 模块 + ShitIDC 上游接口**补齐了跨系统这一侧（见下节），
但 ShitIDC **自身的**商品管理仍然缺少"配置项 / 自定义字段"。

### 2.4 ShitIDC 反超的部分

- **开源可改**：魔方核心加密，ShitIDC 全栈源码。
- **安全能力**：Argon2id、TOTP 两步验证、图形验证码、会话/设备管理、异常 IP 检测、
  登录失败锁定、PII 脱敏 + 审计、API Token 作用域与限流、CSRF。
- **部署形态**：Docker Compose 分层（API/Worker/Scheduler 独立伸缩），魔方是单体 PHP。
- **Webhook**：出站事件推送（HMAC-SHA256 + 时间戳防重放 + 投递记录），魔方靠
  `shd_zjmf_pushhost` 表做上游→下游推送，能力较窄。
- **扩展机制**：wazero WASM 扩展 + 权限 ABI，比 PHP 插件更安全（魔方插件与主程序同权限）。
- **文档**：内置 `/docs/api.md` + OpenAPI 规范 + 交互式 API 文档页。

---

## 3. 本次落地：ShitIDC ↔ 魔方财务 双向对接

### 3.1 方向与取舍

魔方**自身的 openapi**（`app/openapi/*`）不可读，无法可靠复刻。因此采取
**已查证的服务器模块路线**：

- ShitIDC 暴露一套自有接口 `/compat/magiccube/v1/*`（签名：`time+random+token` → md5 大写，
  与魔方明文模块 `bthosts.php:14-23` 完全一致）。
- 魔方侧安装 `zjmf-plugin/shitidc` 模块，用这套签名调用 ShitIDC。

好处：协议两端都是自己的代码，成对演进；不依赖对魔方私有 openapi 的猜测。
代价：需要一个模块文件放到魔方的插件目录。

### 3.2 已实现清单

**ShitIDC 侧**

- `internal/apisign`：魔方签名的 Go 实现（含"按字符串排序"这一易错细节），
  附带与 PHP 语义逐字对照的单元测试。
- `migrations/009_magiccube_upstream.sql`：`upstream_keys`（AES-256-GCM 加密的接口密钥）
  与 `upstream_host_links`（魔方 host ↔ ShitIDC 服务对应关系）。
- `internal/api/api_magiccube.go`：
  - `requireUpstreamKey` 验签中间件（5 分钟防重放窗口、密钥归属校验）；
  - 测试连接 / 商品列表 / 开通 / 查询 / 同步 / 暂停 / 解除暂停 / 删除 / 续费；
  - 开通与续费**真实走 ShitIDC 下单 + 余额支付链路**，因此账目、审计、事件总线全部对齐；
  - `Idempotency-Key` 防重复开通，状态流转复用抢占式状态机。
- 管理端：`/admin/upstream-keys` 增删改查 + 调用记录，前端在
  「供应商 / 魔方上游」页内新增「作为魔方财务的上游」面板（含接入步骤引导）。

**魔方侧**

- `zjmf-plugin/shitidc/`：完整 server 模块（`shitidc.php` + `version.txt` + `templates/`），
  实现 `_MetaData`/`_ConfigOptions`/`_TestLink`/`_CreateAccount`/`_SuspendAccount`/
  `_UnsuspendAccount`/`_TerminateAccount`/`_Renew`/`_Sync`/`_Status`/
  `_ClientArea`/`_ClientAreaOutput`/`_AdminButton`/`_AllowFunction`，
  按魔方约定写回 `host` 表与 `customfields`。

**文档**

- [docs/zjmf-integration.md](zjmf-integration.md)：协议、安装、接口清单、排错表，
  并明确标注"哪些已查证、哪些未查证"。

---

## 4. 仍需补齐的差距（按优先级）

| 优先级 | 差距 | 说明 |
|---|---|---|
| 高 | 商品**配置项**（config options） | 魔方下单页靠它卖"2核4G/4核8G"。ShitIDC 现在只能把差异做成不同商品，商品数量会爆炸 |
| 高 | 商品**自定义字段** | 客户下单时填的额外信息（如系统盘、备注），直接进工单/开通参数 |
| 高 | 库存与限购 | `stock_control` + 单客户最大购买数，卖资源必须能控制超卖 |
| 中 | 免费 / 试用 / 一次性商品 | 拉新常用；试用还依赖"开通 N 天后自动删除" |
| 中 | 升降级 | 有 `shd_upgrades` 的差价折算可参考 |
| 中 | 按量 / 超量计费 | 流量、磁盘超支单独计价 |
| 中 | 多币种独立定价 | 现在只有汇率折算，没有"每币种一套价" |
| 低 | 接口分组与容量分配 | 多个上游按"平均分配/满一个算一个"分摊开通 |
| 低 | 购物车 | 多商品一次结算 |
| 低 | 后付费 | 面向企业客户月结 |

> 建议的下一步：先做**配置项 + 自定义字段**，因为它同时决定了商品模型、
> 下单页渲染、订单明细与开通参数，越晚做改动面越大。
