# 与魔方财务对齐：Go 原生实现路线图

目标：**功能上对齐魔方财务，全部用 Go 实现**，把魔方靠 PHP 插件做的事变成 Go 原生能力。
唯一刻意的区别是语言与运行时：魔方是 PHP + ionCube 加密内核 + 插件生态，ShitIDC 是
Go 全栈开源。

---

## 1. 架构上的根本区别（决定了"插件"怎么写）

| | 魔方财务 | ShitIDC |
|---|---|---|
| 内核 | PHP，ionCube 加密（355/355 文件不可读） | Go，全开源可改 |
| 扩展方式 | 把 PHP 插件丢进目录，与主程序**同进程同权限** | Go 接口 + 编译期注册；沙箱化 WASM 扩展用于不可信代码 |
| 上游对接 | `public/plugins/servers/<标识>/<标识>.php`，函数式约定 | `internal/provider.Provider` 接口 + `cmd/worker` 统一分发 |
| 支付 | `public/plugins/gateways/`，14 个 | `internal/payment` 注册表：易支付 / 支付宝 / Stripe / 微信支付 / PayPal / USDT |
| 短信 | `public/plugins/sms/`，7 个 | `internal/sms` 注册表：阿里云 / 腾讯云 / 赛邮 / 华为云 / 短信宝 / 通用 HTTP |
| 第三方登录 | `public/plugins/oauth/`，4 个 | `internal/oauth` 注册表：GitHub / QQ / 微信 / 微博 / 支付宝 |
| 实名/验证码 | `certification` 6 个、`captcha` 2 个 | 内置图形验证码（纯标准库）；实名认证已补齐（三方核验 + 人工审核通道） |

**"把插件换成 Go"在这里的准确含义**：魔方一个 server 插件对应 ShitIDC 一个
`internal/provider` 实现；一个 gateway 插件对应一个 `internal/payment.Gateway` 实现。
好处是编译进单一二进制、类型安全、无解释器开销，且天然复用 worker 的重试/补偿。

---

## 2. 已经用 Go 实现的部分

### 2.1 基础设施 Provider（对应魔方 server 插件）

| 魔方插件 | ShitIDC Go 实现 | 能力 |
|---|---|---|
| — | `internal/provider/magiccube` | 魔方财务上游：登录换 Token、商品同步、生命周期动作（路径可覆盖） |
| `proxmoxve`（3.7.6 明文模块） | `internal/provider/proxmox` | API Token 认证，LXC/QEMU 克隆开通、暂停/恢复/删除 |
| `MfCloudDisk` 类（虚拟化面板） | `internal/provider/virtualizor` | HMAC 签名管理 API |
| `BtVirtualHost`（宝塔类面板） | `internal/provider/baota` | 建站/停用/启用/删除 + 用量读取；签名与魔方 `bthosts` 模块**逐字节一致**（含与 PHP 写法对照的测试） |
| — | `internal/provider`（Manual） | 手动开通兜底 |

### 2.2 支付网关（对应魔方 gateway 插件）

| 魔方插件 | ShitIDC Go 实现 | 说明 |
|---|---|---|
| `EpayAli` / `EpayWx` / `EpayBank` / `EpayQq` | `internal/epay` | 易支付协议（MD5 签名），一个实现覆盖这些渠道 |
| `AliPay` / `GlobalAliPay` | `internal/payment/alipay` | 支付宝官方 RSA2 电脑网站支付 + 异步通知验签 + 退款 |
| `Stripe` | `internal/payment/stripe` | Checkout Session + webhook 签名 + Refund API |
| `WxPay` | `internal/payment/wechatpay` | 微信支付 APIv3 Native 下单 + RSA 验签 + AES-256-GCM 回调解密 + 原路退款（纯标准库） |
| `Paypal` | `internal/payment/paypal` | Orders v2 下单 + OAuth2 token 缓存 + 回调走官方 verify-webhook-signature 验签 |
| `Epusdt`（USDT） | `internal/payment/usdt` | 签名与官方文档例子逐字节一致；回调验签 + 金额换算 |

### 2.3 其它已对齐的能力

用户/认证（Argon2id、TOTP 两步验证、会话设备管理、登录失败锁定、异常 IP 检测）、
RBAC、商品与多周期价格、**商品配置项**（见 §3）、自定义字段、库存与限购、订单/账单/钱包、
退款（钱包冲正 + 网关原路）、优惠券、推广返佣、代理分组折扣、工单（附件）、
公告、通知中心、邮件模板、多币种、财务统计、出站 Webhook、WASM 扩展、主题 SDK、
CSV 导出、Docker 分层部署。

---

## 2.4 计费模型（本轮补齐）

与魔方 `pay_type` 对齐的四种计费类型：

| 类型 | 行为 |
|---|---|
| `recurring` | 周期付费，下单后需支付 |
| `onetime` | 一次性，只有一个 `onetime` 价格档 |
| `free` | 免费，价格必须为 0；**下单金额为 0 时直接结算并开通，不走支付网关** |
| `trial` | 试用，可收一笔试用费；开通后 `trial_days` 天由 Scheduler 自动回收 |

计费周期从 4 种扩展到 7 种（`hourly` / `daily` / `monthly` / `quarterly` /
`semiannually` / `yearly` / `onetime`），一个商品可同时提供多个周期。

关键实现：

- `store.ValidatePrices` / `ValidBillingCycle` / `NormalizePayType` 是配置的唯一校验入口，
  免费商品写非 0 价、一次性商品缺 onetime 档、周期重复或非法都会被拒绝；
- `Store.CompleteFreeOrder` 只接受**金额为 0 且类型为 free/trial** 的订单，
  其余一律拒绝——它不能变成绕过支付的后门；
- 试用到期回收由 Scheduler 每 5 分钟扫描 `services.trial_ends_at`，
  过期的在 `active`/`suspended` 状态的服务会被终止；
- 免费/试用订单仍然生成正常的订单、账单与服务记录，账目口径不变。

## 2.5 升降级、长周期与条件联动（本轮补齐）

| 能力 | 实现 | 说明 |
|---|---|---|
| 升降级 | `store/store_upgrades.go` + `service_upgrades` 表 | 与 WHMCS / 魔方一致的换算：剩余价值 = 旧周期价 × 剩余天数 / 周期总天数；差价为 0 或负数立即生效不退款，为正则生成升级订单待付款。付款后 `ApplyPaidUpgrade` 切换方案并让 worker 调 `Provider.ChangePackage` |
| Provider 改配 | `provider.Provider.ChangePackage` | magiccube 走可配置的改配路径；proxmox 改 cores/memory；virtualizor 调 `editvs`；不支持的上游返回 `ErrChangePackageUnsupported` 时只做本地记账并留下说明 |
| 长周期 | `biennially` ~ `tenly` | 周期白名单与续费 interval 同步扩展到 10 年 |
| 配置项条件联动 | `config_option_links` + `ResolveConfigSelectionWithLinks` | 选中某候选项才显示/必填目标配置项。**被联动隐藏的配置项即使前端塞了值也不计费**，防止绕过报价 |

## 2.6 按量 / 超量计费（本轮补齐）

对应魔方 `overages_*` 与 DCIM 流量包体系。模型是「每个计量资源一份包含额度，超出按单价计费」：

| 概念 | 字段 | 说明 |
|---|---|---|
| 包含额度 | `disk_included_mb` / `bw_included_gb` | 这个额度内不额外收费 |
| 硬上限 | `disk_limit_mb` / `bw_limit_gb` | 超过则标记待暂停（0 = 不限制） |
| 单价 | `disk_price_cents_per_gb` / `bw_price_cents_per_gb` | 超出部分的价格 |
| 已出账水位 | `billed_disk_mb` / `billed_bw_gb` | **只对水位之上的新增用量收费** |

两条关键设计：

1. **水位机制**（`ComputeOverage`）：重复出账是安全的——用量没涨就一分钱不收。这是
   计费系统最容易出错的地方，因此有专门测试覆盖「重复出账为 0」「只收增量」。
2. **磁盘向上取整、流量按整数 GB**：磁盘超出 1MB 也按 1GB 计（不能让超量变成 0 元账单），
   流量本身就是按 GB 上报的整数，用整数运算彻底避免浮点误差。

用量上报（`ReportUsage`）在一个周期内**只增不减**，防止 Provider 数值抖动导致少收费；
续费时 `ResetUsageCycle` 清零。硬上限只**标记**待暂停，由调度器执行，让计费与生命周期解耦。

## 2.7 多币种独立定价（本轮补齐）

对应魔方 `shd_pricing` 的「每币种一行」。`product_prices` 的唯一键本来就是
`(product_id, billing_cycle, currency)`，所以表结构早就支持；之前缺的是**下单时用哪个
币种**的解析规则，以及后台维护多币种价格的入口。

解析顺序（`resolveOrderCurrency`）：

1. 调用方显式指定的币种——**必须是该商品在该周期上确实有价格的币种，否则报错**。
   宁可拒绝也不能悄悄回退到另一个币种，那等于用错价格卖货。
2. 否则按商品在该周期上的可售币种（按币种代码排序取第一个）。
3. 价格的选取是 `(商品, 周期, 币种)` 三元组精确匹配，**不会用汇率去折算**。

修掉了两个真实缺陷：

- **`SetProductPrices` 会把所有币种的价格一起下架** —— 给商品加一个美元价，人民币价
  就被删了。现在只动指定币种的行。
- **`validatePrices` 把「同周期不同币种」判定成重复** —— 多币种下同一个周期在每个币种
  上都应该有一条，唯一键是（周期，币种）。

钱包按币种隔离：用人民币钱包支付美元订单会失败（而不是扣人民币），有测试覆盖。

## 2.75 PayPal 与 USDT（本轮补齐）

**PayPal**（`internal/payment/paypal`）：Orders v2 下单，回调验签走官方
`verify-webhook-signature` 接口而不是本地推算签名串。

**为什么回调不本地验签**：本地推算要自己算 CRC32、自己拉证书链，任何一步写错都只表现为
「验签失败」而极难定位。多一次出网请求换取确定性，对回调这个量级完全值得。

另外两点：access token 会**缓存**（每笔订单都取一次 token 又慢又容易被限流，有测试断言
3 笔订单只取 1 次 token）；金额解析走**字符串**而不是浮点（支付金额不能有浮点误差，
`0.005` 这类边界都有测试）。

**USDT**（`internal/payment/usdt`）：对接 Epusdt / BEpusdt。签名算法取自官方文档
「签名算法」——非空参数按 ASCII 字典序拼成 `k=v&k=v`，**末尾直接拼接 api token**
（不加分隔符，也不是 HMAC），再取 MD5 小写。文档给的例子已经写进测试，签名必须复算出
同一个值，所以这套实现是**有据可依的**，不是猜测。

## 2.8 短信通道与短信验证码（本轮补齐）

对应魔方 `public/plugins/sms/`（Aliyun / Qcloudsms / Submail 等）。分两层：

**通道抽象**（`internal/sms`）：`Provider` 接口只做一件事——把验证码发出去。
限频、验证码存储、错误计数都在 store 层，通道实现保持无状态，方便替换与测试。
已实现**阿里云短信**（Dysmsapi 2017-05-25）。

阿里云签名有三处与标准 URL 编码不同的地方，单独抽成 `aliPercentEncode` 并配了测试：
空格编成 `%20` 而不是 `+`、`*` 编成 `%2A`、`~` 必须保持原样而不是 `%7E`。
写错任意一处，线上表现都是"签名校验失败"，非常难查——所以用测试钉死。

**验证码流程**（与邮箱验证码同构）：只存哈希、按 (手机号, 用途) 限频、错 5 次锁定、
10 分钟过期。差别在于**短信是花钱的**，所以额外做了一层：

- **按手机号的风控**：1 分钟 1 条、1 小时 5 条、1 天 10 条，超了直接拒绝
  （防止平台被当成短信轰炸的跳板）
- **发送流水表** `sms_messages`：能回答"谁在什么时候给哪个号发了多少条"，
  风控只统计**成功**的发送（失败的不花钱）
- 先入库再发送：发送失败则整个请求失败，不会占着限频窗口却不给用户验证码

## 2.90 后付费（本轮补齐）

对应魔方 `pay_method = postpaid`。授信客户先开通、后付款，到期未付则暂停服务。

### 授信是显式授予的

`postpaid_enabled` 默认 **FALSE** —— 注册不会自动获得赊账能力，额度与账期都由管理员设置。
这是整个功能的核心风险点，所以配套三条约束：

- **额度占用实时算**，不另存冗余字段。占用 = 未付的后付费订单总额。存冗余字段迟早会出现
  「额度字段与实际占用不一致」，那是最难查的一类账目问题。
- **降额/停用前必须结清欠款**。有测试验证：还有未付订单时停用授信会被拒，而不是把风险
  留在账上又失去追索依据。
- **额度变更全部留痕**（`credit_events`：enable / grant / revoke / disable），
  出坏账时能复盘是谁在什么时候提的额。

### 并发不能把额度用超

`checkPostpaidTx` 在订单事务里 `SELECT ... FOR UPDATE` 锁住用户行，让并发下单**串行**判断额度。
不锁的话两笔订单会各自看到「还有余额」而一起通过。

有测试验证：额度只够 3 单时并发发起 10 笔订单，**恰好 3 笔成功**，占用正好等于额度。

### 与预付费共用同一条下单路径

后付费仍然生成**正常的订单与发票**，只是订单记 `pay_method='postpaid'`、发票到期日换成账期。
库存占用、配置计价、组折扣、专属价、优惠码完全没有分支 —— 这样退款、对账、统计等既有逻辑
一行都不用改。

## 2.89 购物车多商品结算（本轮补齐）

对应魔方 `shd_cart_session`。

### 购物车不存价格

这是整个设计的核心决定。每次读取购物车都用与下单**完全相同**的计价逻辑重算，所以
组折扣调整、专属价变更、商品下架、库存变化都会在下次读取时立刻体现，而不是等到付款
才暴露。存价格的话就会出现「加购时 100 元、结算时按 100 元卖但市价已经 120」。

有一条测试专门验证这一点：给用户挂上 5 折组之后重新读购物车，单价必须立刻从
10000 变成 5000。

### 一次结算 = 一个批次

`checkout_groups` 负责「一起付款」，订单仍然一笔一单（发票、退款、升降级都按单处理）。
结算在**一个可串行化事务**里完成：要么所有明细都成单，要么一条都不成 —— 部分成功会让
用户付了钱却少一台机器。

结算只生成**待付款**订单，不收钱。付款走 `PayCheckoutGroup`。

### 合并付款的三条硬约束

1. **整批一次扣款**，而不是逐单循环。逐单付款在中间失败会留下「付了 3 单、第 4 单失败」
   的半成品状态。有测试验证余额不足时：一笔订单都没被标记已付、余额一分不动。
2. **扣款额与约定金额比对**。下单时把总额写进 `checkout_groups.total_cents`，
   付款时再比一次；不一致说明两次读取之间价格被改动过，此时宁可停下来让用户确认，
   也不能按旧价静默扣款。
3. **幂等**。重复调用返回既有结果，不重复扣款（有测试断言第二次调用余额不变）。

### 顺带修掉的一个结构性问题

改造中发现 `payOrderWithWalletOnce` 会**自己开事务**，所以合并付款里逐单调用它并不
原子 —— 失败的订单不会跟着回滚。为此把它拆成 `debitWalletTx`（扣款）+
`settlePaidOrderInTx`（在**调用方事务里**收尾：记流水、核销发票、续费或建服务），
单品付款与合并付款现在共享同一段收尾逻辑。

拆分时踩到一个真实陷阱：**账本里支出必须是负数**。原实现写的是负数金额，
我第一次抽 helper 时写成了正数，`TestConcurrentWalletDeduction` 与
`TestRefundReversalKeepsLedger` 立刻报错（"sum of debits != amount spent"）。
符号约定现在写在函数注释里：支出记负、入账记正，这样「所有负数流水之和」就是总共花掉的钱。

## 2.88 第三方登录（本轮补齐）

对应魔方 `public/plugins/oauth/`。统一走标准 Authorization Code 流程：

```
1. GET /auth/oauth/:provider/start     -> 302 到平台授权页（带一次性 state）
2. 平台回调 GET /auth/oauth/:provider/callback?code=&state=
3. 校验并消费 state，用 code 换资料，找绑定 / 建号 / 要求绑定
```

各平台差异收敛在 `oauth.Provider` 实现里，主流程只认 `Identity`。已实现 **GitHub**
（协议完全公开，测试能对着假服务器跑完整流程）。

### 三个安全决定

**state 一次性且存库**。`ConsumeOAuthState` 是 `DELETE ... RETURNING`：取出即删除，
重放立刻失败。另外还会校验 state 所属的通道，防止拿 A 通道的 state 走 B 通道。

**跳转地址必须过滤**。`SafeRedirectTo` 只接受单个斜杠开头的相对路径，并拒绝
`//evil.com`、`/\evil.com`、控制字符等 —— 写错就是开放重定向漏洞。测试列了 9 种
攻击写法逐一断言。

**身份不能被静默改绑**。绑定用 `ON CONFLICT ... DO UPDATE ... WHERE user_id = excluded.user_id`：
已绑给别人时 `RowsAffected()==0`，直接报错。这挡住了"用同一个第三方账号把别人的号抢过来"。
测试验证原绑定完好、攻击者一无所获、绑给同一个人是幂等的。

### 没有密码的账号

自动注册的账号 `password_hash` 写哨兵值 `!oauth`：列是 NOT NULL，但该值不可能等于任何
bcrypt 结果，登录时比对必然失败。`UserHasPassword` 把它当作"没有密码"。

由此带出一条约束：**解绑时必须至少留一种登录方式**。没有密码又只有这一个绑定时
解绑会被拒绝，否则账号永久失联。有测试覆盖。

另外，第三方返回的邮箱若**已存在**账号，`CreateOAuthUser` 直接报错要求先去绑定，
而不是凭空建号或静默登进别人的账号。

## 2.87 实名认证（本轮补齐）

对应魔方 `public/plugins/certification/`。分两层：通道（Provider）负责把「姓名 + 证件号」
送三方核验，本地的纯函数负责预处理（校验身份证校验位、解出生日期与性别、掩码）。

### 隐私是硬约束

**身份证号与真实姓名绝不落明文**：

| 存什么 | 怎么存 |
|---|---|
| 展示 | 掩码：`张*三` / `110***********1234` |
| 去重 | `HMAC-SHA256(主密钥, 值)` |

用 **HMAC 而不是裸 SHA256**：证件号空间有限且规律性强，裸哈希可以被穷举反查；
带上密钥的 HMAC 在密钥不泄露的前提下不可逆。有一条测试专门验证「同一证件号在不同
主密钥下指纹不同」。没配主密钥时提交会**明确报错**，而不是退化成存明文。

另外有一条测试**直接读整行**，逐列断言不含明文姓名、证件号与出生日期字符串 ——
隐私约束必须由测试守住，而不是靠"我记得没存"。

### 本地先算校验位

中国身份证第 18 位是 GB 11643-1999 定义的校验位。本地先算一遍能挡掉绝大多数
「打错一位」，省下一次付费的三方调用。测试覆盖：正确号码通过、改校验位被拒、
改中间一位被拒、月份 13 被拒、**2 月 30 日被拒**。

最后这条有个陷阱：`time.Date` 会把 2 月 30 日「归一化」成 3 月 2 日，不比对就会
放过非法日期。代码里显式回读年月日比对，测试同时验证「2024-02-29 是闰年真实存在」。

### 一人一证

`approved` 状态上建了唯一索引：同一个证件号不能认证两个账号，防止拿别人的证件开小号。
`pending` 不受限制（还没审核的重复提交只是排队）。用户填错了可以重新提交覆盖，
但**已通过的记录不允许被静默覆盖**，必须先由管理员驳回。

## 2.85 客户组按产品差异定价（本轮补齐）

对应魔方 `shd_user_product_bates`。原来只有 `user_groups.discount_percent` 这个全局折扣，
现在多了一层「某个组 + 某个商品 + 某个周期 + 某个币种」的**专属固定价**。

价格优先级（`ApplyGroupPrice`，纯函数、表驱动测试）：

1. 组专属固定价
2. 标价 + 组折扣
3. 标价

三个关键决定：

- **专属价之上不再叠加组折扣**。固定价就是固定价，否则运营很难解释「为什么标 50 元最后
  收了 45 元」，客户也会觉得被多扣钱。
- **折扣只作用于商品本身，不作用于配置项加价与初装费**。那两项是成本项，打折卖会亏。
  有专门测试锁死这一点。
- **脏数据不会算出负价**：`discount_percent > 100` 被夹到 100%；专属价高于标价时展示上
  不出现「负优惠」。

### 修掉的一个真实缺陷：组折扣被重复扣减

改造过程中发现原实现把组折扣**扣了两次**——`subtotal` 已经是「折后单价 × 数量 + 配置加价」，
后面又减了一次 `groupDiscount`。表现是客户被多扣钱（标价 100 元打 7 折，实际收 40 元）。
数据库测试把它抓了出来，因为订单总额对不上。

顺带修了 `orders` 的 `RETURNING` 子句漏掉 `discount_cents` / `coupon_code`，
导致创建订单返回的结构体里这两个字段永远是零值（数据库里其实是对的）——
这类「写对了但读不对」的问题同样会误导上层逻辑。

## 2.9 接口分组与容量分配（本轮补齐）

对应魔方 `shd_server_groups` 的「平均分配 / 满一个算一个」。商品可以绑定到**分组**
而不是单个接口；开通时由核心按策略挑一个还有容量的接口。好处是加机器只要往分组里
加接口，商品配置不用改。

三种策略（`pickProvider`，纯函数、表驱动测试）：

| 策略 | 行为 |
|---|---|
| `least_loaded` | 选当前承载服务数最少的；**不限容量的接口优先**于「快满了」的接口 |
| `fill_first` | 凑满一个再下一个（优先继续填已经有负载的接口） |
| `round_robin` | 按已开通数量取模轮转 |

几个刻意的设计：

- **容量判定**：`max_services = 0` 表示不限；负载只统计**还活着**的服务
  （`terminated`/`failed` 不占容量），否则接口迟早会因为历史服务被判定为满。
- **排序稳定**：统一按「权重降序 → 负载升序 → ID 升序」，同一组数据每次挑到同一个接口。
  排查问题时不会因为顺序抖动而困惑。
- **挑不出来就明确失败**：分组全满或全停用时，`ClaimServiceForProvisioning` 把服务放回
  `pending` 并返回错误提示扩容，**而不是硬塞到一个已经满的接口上**。
- **选中结果落到服务行**：`services.provider_id` 与 `provider_group_id` 都记下来，
  后续暂停/删除/续费找同一台机器；重试时也不会在第二台机器上重复开通。

## 3. 本轮新增：商品配置项 / 自定义字段 / 库存限购

这是此前与魔方差距最大的一块（魔方下单页靠它卖"2核4G / 4核8G"）。

### 3.1 数据模型（`migrations/010_config_options.sql`）

```
config_groups / config_group_links      配置项分组，可挂到多个商品
config_options                          配置项：type 1下拉 2单选 3开关 4数量
config_option_values                    候选项：price_cents 加价 / setup_cents 初装费
product_custom_fields                   自定义字段：text/textarea/dropdown/password
products.stock_control/stock_qty/sold_count/allow_qty/max_per_customer
order_items.config_cents/setup_cents/config_selections/custom_fields
```

与魔方的差异：魔方把配置项价格塞进多态的 `shd_pricing`；ShitIDC 把加价直接挂在子项上，
少一层间接，也少一类查询。

### 3.2 计价是服务端唯一入口

`internal/store.ResolveConfigSelection` 是**唯一**的配置项计价函数，规则：

- 未知配置项 / 未知子项 ID → 拒绝（挡住前端改价）
- 子项必须属于该配置项 → 拒绝跨项引用
- `required` 必须选；数量型必须落在 `[qty_min, qty_max]`
- 开关型未选 = 关（加价 0，但仍在订单里留一行"否"，便于售后核对）
- 数量型：数量 × 子项单价

`CreateOrderWithConfig` 在订单事务内重算：基础价 × 数量 + 配置加价 × 数量 + 初装费，
再依次套用**代理组折扣 → 优惠码**。前端报价仅用于展示。

测试见 `internal/store/store_config_options_test.go`（算价、拒单、标签落库三组）。

### 3.3 库存与限购

`checkStockAndQtyTx` 与订单同一事务：`stock_control` 开启时校验
`stock_qty - sold_count >= quantity`；`allow_qty=false` 时一次只能 1 件；
`max_per_customer>0` 时按该用户现有未终止服务数限购。下单成功即 `sold_count += quantity`，
超卖在事务层面不可能发生。

### 3.4 界面

- 下单弹窗：按类型渲染下拉/单选/开关/数量，实时显示配置加价与初装费、剩余库存；
  自定义字段按 text/textarea/dropdown/password 渲染。
- 管理端接口：`/admin/products/:id/config-options`、`/admin/products/:id/custom-fields`。

---

## 4. 仍未对齐的差距

魔方参考源里**可读**的插件协议已全部用 Go 对齐（见 §9）。剩下的只有两类，
且都有明确的客观障碍：

| 差距 | 障碍 | 说明 |
|---|---|---|
| 域名注册类 Provider（`WestDomain` `ZgsjDomain`） | 参考源 **ionCube 全加密** | 协议不可读。按本项目「识别不了的不编造」原则（§7.2），不凭记忆猜测协议——猜测出来的域名 Provider 直接操作真实域名与续费金额，错一步就是真实损失 |
| DirectAdmin / MfCloud 系列（`DirectAdmin` `MfCloudDisk` 等） | 参考源 ionCube 全加密 | 同上；魔方 3.7.6 明文包里只有 bthosts/nokvm/proxmoxve/wlkanglepro 四个模块 |

核心闭环（卖货 → 收款 → 开通 → 续费 → 升降级 → 退款 → 计费）已经全部打通。
已完成的项目见下一节，避免重复排查。

## 4.5 已完成清单

以下是**已经补齐、不再是差距**的项目，避免重复排查：

**P0 核心闭环**

- 升降级（`internal/store/store_upgrades.go`）—— 剩余价值折算 + 付款后调 `Provider.ChangePackage`
- 配置项条件联动（`config_option_links`）—— 被联动隐藏的配置项即使前端塞了值也不计费
- 2~10 年长周期（`biennially` ~ `tenly`）
- 免费 / 试用 / 一次性商品

**P1 渠道与计费**

- 微信支付 APIv3（`internal/payment/wechatpay`）、PayPal、USDT（Epusdt）
- 短信通道（`internal/sms`，阿里云）+ 短信验证码与三级风控
- 宝塔面板 Provider（`internal/provider/baota`）
- 实名认证（`internal/certification`）—— 三方核验 + 人工审核通道
- 第三方登录（`internal/oauth`）—— 标准授权码流程 + state 防重放 + 安全解绑
- 按量 / 超量计费（`internal/store/store_metered.go`）—— 水位机制保证同一段用量只收一次钱

**P2 规模化运营**

- 多币种独立定价（`internal/store/store_multicurrency.go`）—— 不做汇率折算
- 客户组按产品差异定价（`internal/store/store_user_prices.go`）
- 接口分组容量分配（`internal/store/store_provider_groups.go`）—— 三种策略
- 购物车多商品结算（`internal/store/store_cart.go`）—— 价格实时重算 + 整批原子付款
- 后付费（`internal/store/store_postpaid.go`）—— 显式授信 + 并发不超额度 + 变更留痕
- 第三方登录通道补齐：QQ / 微信 / 微博 / 支付宝（`internal/oauth`）
- 短信通道补齐：腾讯云 / 赛邮 / 华为云 / 短信宝 / 通用 HTTP（`internal/sms`）
- 支付网关补齐：虎皮椒 / GoAllPay / OCGC（`internal/payment`，见 §9）
- 基础设施 Provider 补齐：NOKVM / kangle（`internal/provider`，见 §9）
- 实名核验通道补齐：阿里云二要素（`internal/certification/alitwo.go`，见 §9）

---

## 5. 建议的推进顺序

1. **配置项管理界面**（接口已就绪，只差编辑器）—— 补齐后"卖可配置商品"闭环完成。
2. **免费 / 试用 / 一次性 + 更多计费周期** —— 商品模型一次性扩到位，改动面集中在价格表。
3. **升降级** —— 依赖配置项与价格表，放在 1、2 之后最省事。
4. **宝塔 BtVirtualHost Provider（Go）** —— 国内主机销售最高频的上游。
5. **短信 + 第三方登录** —— 注册转化与账号体系。
6. **按量计费** —— 改动最大，放最后。

每补一项都应有对应的 Go 单元测试（计价与状态流转部分尤其），延续
`store_config_options_test.go` 与 `apisign_test.go` 的做法。
---

## 6. 管理后台体验（本轮补齐）

### 6.1 不再要求手抄 UUID

后台原先在多处让管理员去列表页复制 UUID（给用户分组、指定接口密钥归属）。抄错一位
就会静默作用到别人身上，而且没法在执行前确认选的是不是对的人。

现在统一用 `EntityPicker` 组件：**点开输入框 → 搜索 → 看到关键信息 → 选中**。
后端 `GET /admin/search?q=&kind=` 一个入口查四类对象：

| 类型 | 可用关键词 |
|---|---|
| 用户 | 邮箱（模糊）、UID（纯数字）、UUID |
| 商品 | 名称（模糊）、UUID |
| 服务 | UUID、UID、所属用户邮箱 |
| 订单 | UUID、UID、所属用户邮箱 |

返回统一的 `{kind,id,label,sub}`，所以一个弹窗组件就能渲染全部类型。

### 6.2 点击用户查看全部信息

`GET /admin/users/:id/detail` 一次返回余额（按币种）、机器、订单、授信占用与逾期。
管理员排查问题要看的本来就是跨表的，让前端发四五个请求既慢又要处理部分失败。

抽屉里可直接：余额调账、查看实名资料、禁用/启用账号。列表行整行可点，行内按钮
`@click.stop` 避免重复弹窗。

### 6.3 站内公告独立成页

撰写公告需要横向空间与实时预览，挤在两栏小卡片里既写不长也看不清排版。现在左侧编辑、
右侧实时预览用户看到的样式，列表整宽、正文两行截断、支持搜索标题与正文。

### 6.4 公告可以点开看全文

原先公告只有仪表板那一小块面板：正文挤在两行里，长了根本读不了，标题旁的「共 N 条」
还错误地指向 `/`（点了原地不动）。现在拆成两层：

- **仪表板**：只放最新 5 条做入口，每条整行可点，摘要单行截断（长公告不会把首页撑变形），
  右上角「查看全部（N）→」进列表页。
- **`/announcements`**：全部公告的列表页，卡片整卡可点，支持搜索标题与正文、只看置顶。
- **`/announcements/:id`**：详情页。正文按空行分段渲染，行高 1.85（维护公告常有步骤，
  密排很难读），底部给出「其它公告」接着看。

配套加了 `GET /announcements/:id`。**下线的公告对用户等于不存在**（返回 404）——
否则撤下的内容还能被翻出来。有测试覆盖这一点，以及正文换行必须原样保留
（详情页靠换行分段，压平就毁了排版）。

### 6.5 后台路径可配置

两个独立开关：

- 后端 `ADMIN_PATH`（默认 `/admin-panel`）：决定 API 前缀。`/admin` **始终**作为
  兼容镜像挂载，老链接与已发出的文档不会失效。路由表只写一次
  （`registerAdminRoutes`），挂到两个前缀上，不会出现「某个前缀少了几个接口」。
- 前端 `VITE_ADMIN_PATH`（默认 `/admin`）：决定浏览器地址栏路径，构建期生效。

`NewRouter` 会**再归一化一次**后台路径——`Config` 未必经 `Load()` 构造（测试、内嵌调用），
空值会让后台路由挂到根路径并与用户路由直接冲突。

---

## 7. 插件市场与魔方插件导入（本轮补齐）

对应魔方财务的「应用商店」+「把插件丢进目录就能用」的扩展生态。

### 7.1 插件市场

市场 = 一个可自托管的 JSON 索引（`docs/marketplace.md`）+ 安装包，支持三类包：
`extension`（WASM 扩展）、`theme`（主题）、`zjmf-plugin`（魔方 server 插件）。
安装扩展/主题完全复用既有上传校验链（manifest / WASM 魔数 / Zip Slip / 权限白名单），
市场只是多回答"包从哪来"；下载走 SSRF 防护并支持 sha256 校验。

管理接口：`GET /admin/marketplace`（索引 + 本机安装状态标注）、
`POST /admin/marketplace/install`、`GET|PUT /admin/marketplace/settings`。

### 7.2 魔方 server 插件导入（转换器 + 声明式上游运行时）

魔方插件是 PHP，不能运行；但经典 server 模块高度公式化。方案是**静态转换**：

```
魔方插件 zip ──internal/zjmfimport（解析 PHP）──▶ ProviderSpec JSON
            ──internal/provider/custom（执行规格）──▶ custom 供应商
```

- 转换器识别三种已知签名（bthosts/nokvm/wlkanglepro 形态，wlkanglepro 的
  `s=md5(a+token+r)` 约定已对源码核实）、业务码判据、全部生命周期动作路径、
  `_ConfigOptions` 两种 options 写法、`$params → {{模板}}` 请求体映射；
- 识别不了的不编造：动作留空 + warnings 明示，导入后在「查看规格」里编辑 JSON 补齐；
- PVE ticket 类无法静态转换的模块明确拒绝并指向内置 proxmox 供应商；
- 运行时按规格发请求：签名逐字节对齐（有测试钉死）、成功判据数字宽容比较、
  失败文案透出、开通号按 `instance_id_path` 提取、缺失动作诚实报错。

配套改动：

- `Renew(ctx, RenewRequest{InstanceID, ExpiresAt})`：续费把新到期时间传给上游
  （对应魔方 `_Renew` 的 `nextduedate` 约定），全部内置 Provider 已适配；
- worker 开通时携带**开通上下文**（订单配置项 / 自定义字段 / 邮箱，魔方
  `$params['configoptions']` 的等价物），配置项按 `config_options.provider_key`
  映射上游参数名（migrations/022），开关传 1/0、数量传数量、下拉传子项标签；
- CLI `cmd/zjmfimport`：离线转换 zip/目录 → 规格 JSON；
- 四个随魔方发布模块（bthosts/nokvm/wlkanglepro/proxmoxve）实测通过
  （参考源码仅用于冒烟验证，不入仓库；单测用按契约文档撰写的原创夹具）。

详见 `docs/zjmf-plugin-import.md`，含模板变量速查与诚实的限制清单。

---

## 8. 短信/登录渠道补齐与账号手机号（本轮补齐）

对应魔方 `public/plugins/sms/` 全部 7 个通道里的 6 个（idcsmart/idcsmartpro 是
厂商自营付费通道，用「通用 HTTP」覆盖同类自建网关），以及 `public/plugins/oauth/`
的全部 4 个通道。至此 §4 的「渠道数量」差距只剩支付与域名注册两类。

### 8.1 短信通道（`internal/sms`，全部纯标准库）

| 通道 | 协议 | 关键点 |
|---|---|---|
| `qcloudsms` 腾讯云 | SMS 2021-01-11，API 3.0 | **TC3-HMAC-SHA256 签名手写**（kDate→kService→kSigning 逐层派生），测试向量独立用 Python 复算钉死 |
| `submail` 赛邮 | message/send.json 表单 | 国内/国际按号码自动分流到两个端点；`signature=appkey` 免签名模式 |
| `huaweicloud` 华为云 | batchSendSms/v1 | **WSSE UsernameToken 头**：PasswordDigest 是 `Base64(SHA256摘要的十六进制串)`，不是 `Base64(原始摘要)`——与魔方 PHP 插件 `base64_encode(hash('sha256',...))` 逐字节一致，两种写法差一个就是「签名错误」，向量测试钉死 |
| `smsbao` 短信宝 | GET + 密码 MD5 | 状态码表与插件 statusStr 一致（"0" 成功，其余翻成人话） |
| `generic` 通用 HTTP | 模板化请求 | `{{phone}}/{{code}}/{{content}}/{{secret:KEY}}` 占位符 + `success_keyword` 判据；**未引用的凭据绝不进请求体**，遗留占位符清空 |

共同设计：

- **国内/国际自动分流**：11 位以 1 开头视为大陆号（腾讯云/华为云补 `+86` 成 E.164），
  其余视为已带国家码；国际子通道凭据缺失时**明确报错**，而不是拿国内凭据硬发。
- 密钥（Secret）与非敏感配置（Fields）继续分库存储，凭据加密入库。

### 8.2 第三方登录通道（`internal/oauth`）

| 通道 | 授权页 | 说明 |
|---|---|---|
| `qq` | graph.qq.com | token 默认返回 `a=1&b=2` 文本（JSONP 形态也有兜底）；`ret` 是数字，必须按数字判 0，按字符串判会漏掉业务错误（测试抓出来的） |
| `weixin` | open.weixin.qq.com/qrconnect | 四个通道里唯一回 `unionid` 的，透传到 Identity 供跨应用识别 |
| `weibo` | api.weibo.com | **修掉魔方参考实现的一个错误**：它把 access_token 当 openid 回传，token 一换代 subject 就漂移、绑定关系失效——正确主键是响应里的 `uid`，测试断言 subject ≠ token |
| `alipay` | openauth.alipay.com | 回调参数是 `auth_code`（API 层兼容读取）；网关协议与网站支付同构，**签名/密钥解析抽到 `internal/alipaykit` 共享**，支付与登录不再各有一份 |

支付宝响应不做本地验签（与参考实现一致，TLS 已保证传输安全），网关地址固定官方
HTTPS 端点、测试可覆盖。

### 8.3 顺带修掉的后端缺陷

- **更新支付宝登录通道会被自家校验卡死**：管理端保存时密钥留空表示沿用旧值，
  占位值映射里漏了 `app_private_key`，导致不改私钥就存不了其它配置。
- **绑定手机号接口缺失**：`SetUserPhone` 一直存在但没有任何 API 调用它——
  短信验证码的 `bind` 用途悬空。补上 `POST/DELETE /profile/phone`（验证码消费
  与绑定同请求内完成，`users.phone` 唯一索引挡住一号多绑）。

### 8.4 界面（接口早已就绪，本轮补编辑器）

- **管理端 → 系统设置**：新增「短信通道」「第三方登录」两个面板。每个通道的
  表单字段由前端元数据描述（与各通道 `Validate` 必填项一一对应），凭据字段
  密码框渲染；短信面板带最近 20 条发送流水（排查盗刷）。
- **登录页**：启用中的通道自动出现第三方登录按钮（拉 `/auth/oauth/providers`），
  回调失败时后端带回的 `?oauth_error=` 以消息条展示。
- **个人中心**：手机号绑定/解绑（带发送倒计时），第三方账号绑定列表与解绑；
  「没有密码的 OAuth 账号」在解绑最后一个第三方账号前有明确提示。

---

## 9. 支付网关 / 基础设施 Provider / 实名通道收尾（本轮补齐）

魔方参考源里剩余的**明文**插件协议全部对齐完毕。这一轮之后，魔方插件生态里
所有协议可读的通道在 ShitIDC 都有 Go 原生实现。

### 9.1 支付网关（`internal/payment`）

| 网关 | 协议 | 关键点 |
|---|---|---|
| `xunhupay` 虎皮椒 | `payment/do.html`，MD5 签名（secret 直接拼在串尾，无分隔符） | 参考实现响应验签写成 `$hash !== $hash`（恒 false，验签形同虚设），Go 实现真校验；金额走分→两位小数字符串的整数换算；一个方法覆盖 alipay/wechat 两个渠道（wxpay 自动归一化） |
| `goallpay` GoAllPay | AllPay v5 通用接口 `/api/createorder`，SHA256 签名 | 三个魔方插件（Ali/Wechat/Unionpay）收敛为一个方法，paymentMethod 映射 alipay_cn / wechat_pay / unionpay；detailInfo 是 base64 的商品 JSON；userIP 等信息字段不在 Prepared 里，签名规则排除空值故直接省略 |
| `ocgcpay` OCGC（酷云） | 两步会话式：`msc/user/login` → `msc/txn/request` | 请求体是 **JSON 数组包裹单对象且字段顺序固定**（签名覆盖原始字节，Go 用声明序 struct 保证）；x-apsignature 为 RSA 签名大写 hex，算法与参考实现一致（PHP openssl_sign 默认 SHA1）；响应验签覆盖**含方括号的原始字节**；回调 body+sign 形态。qrcodeUrl 非 http 时明确报错——本系统的支付是整页跳转，二维码图地址无法承载 |

三个网关都接入了既有的 `VerifyNotify` → 金额比对 → 原路记账闭环，测试对着
假服务器跑全流程（下单参数、签名复算、响应验签、回调篡改拒绝）。

### 9.2 基础设施 Provider（`internal/provider`）

| Provider | 协议来源 | 关键点 |
|---|---|---|
| `nokvm` NOKVM 虚拟化 | `servers/nokvm/nokvm.php`（明文） | 签名与宝塔同源但排序的是**值**（time/random/token 字符串排序后拼接）；开通 `/api/virtual`（expire_time 固定 2999，到期由本系统回收）、暂停/恢复/删除/改配（PUT `/api/virtual/{id}`，只提交变化的键）；主 IP 按 `ip_address_id` 分流；win 镜像用户名 administrator |
| `wlkangle` kangle 虚拟主机 | `servers/wlkanglepro/wlkanglepro.php`（明文） | `s = md5(a + token + r)`；开通 add_vh（init=1，产品ID方式只带 product_id）、改配 add_vh+edit=1、暂停 update_vh status=1/0、删除 del_vh；**续费=解除暂停**（与参考实现 _Renew 一致） |

两者都实现了完整 `Provider` 接口并接入 worker 与管理端分发器；配置模板
（含此前漏掉的宝塔）已加入后台下拉。

### 9.3 实名核验通道（`internal/certification`）

- **`alitwo` 阿里云身份证二要素**（对应魔方 `certification/alitwo`）：阿里云云市场
  简单认证模式，`Authorization: APPCODE` 头 + GET `?idCard=&name=`；
  `status=="01"` 判一致，失败 msg 带出，traceId 作为凭证。
- 顺带补上了**实名通道管理接口**（此前 `certification_providers` 表与健康度
  追踪都在、但没有任何 API 能创建通道——同绑定手机号的缺口模式）：
  `GET/POST /admin/certification-providers`、`:id/default`、`:id/delete`，
  凭据加密入库；后台设置页新增「实名核验通道」面板。

### 9.4 明确不做的部分（及原因）

魔方 CBAP 插件包里的 `WestDomain` / `ZgsjDomain`（域名注册）与
`DirectAdmin` / `MfCloudDisk` / `MfCloudIp` / `MfDcimCabinet` 全部是
**ionCube 加密**的——协议不可读。这些通道涉及真实域名与扣费操作，
凭记忆猜测协议的风险远大于收益；按 §7.2「识别不了的不编造」原则明确跳过，
将来拿到明文参考或官方文档时按同样的 Provider 模式接入即可。

### 9.5 收尾状态

至此魔方参考源中协议可读的全部插件类型（gateway 14 个、server 4 个、
sms 7 个、oauth 4 个、certification 6 个）在 ShitIDC 都有对应实现或等价能力。
剩余差距只有 §4 表里两类 ionCube 加密的模块。

---

## 10. 邮件通道与附属插件补齐（本轮补齐）

§9 的「收尾」只覆盖了五类插件；复扫 CBAP 插件包与安装目录后发现，
魔方还有 `mail`（邮件通道）一整类，以及 `captcha` / `oss` / `addon` 等
此前没有逐项对照的类别。本节起按类别补齐。

### 10.1 邮件通道（`internal/mail`）

此前 ShitIDC 只有内置 SMTP 一条发信路径。现在与短信同构做成注册表：
`mail_providers` 表 + 后台「邮件通道」面板，一个站点可配多条、一条默认、
凭据加密入库、发送结果记健康度（last_ok_at / last_error）。

| 通道 | 协议 | 关键点 |
|---|---|---|
| `alimail` 阿里云邮件推送 | RPC 风格 POST dm.aliyuncs.com，`Action=SingleSendMail` | 签名 HMAC-SHA1 与阿里云短信同源；编码三差异（空格→%20、*→%2A、~ 保持）与短信实现一致 |
| `subemail` 赛邮邮件 | POST api.mysubmail.com/mail/send 表单 | `signature=appkey`；错误码表翻成人话 |
| `btmail` 宝塔邮局 | POST {面板}/mail_sys/send_mail_http.json | 成功判据是布尔 `status===true`；面板默认自签证书，`insecure_tls` 默认可跳过（可显式关闭） |
| `generic` 通用 HTTP | 模板化请求 | `{{to}}/{{subject}}/{{body}}/{{secret:KEY}}` 占位符；未引用的凭据占位符清空 |

**解析顺序：启用中的通道优先，未配置通道时回退内置 SMTP**。回退只在
「没有通道」时发生，通道发送失败不会静默换身份重发——否则同一站点用两个
发件人发信，收件人看到的来源会漂移。注册/登录页的「邮件服务是否可用」判断
同步改为通道 OR SMTP，避免只配了通道却提示未配置。

顺带修掉一个真实缺陷：后台短信/第三方登录面板按 `public_id` 找通道 ID，
而接口序列化的是 `id`，导致「设为默认 / 停用 / 删除」实际传 `undefined` 全部 404。
同类漏改还有一处：重置密码接口的邮件可用性判断只认 SMTP、发信路径也绕过
通道解析，只配邮件通道时会被误报「未配置」并以 SMTP 直发失败——已改为与
其余流程一致的「通道优先、SMTP 兜底」。

### 10.2 第三方登录补齐（`internal/oauth`）

§8 只对齐了安装目录里的 4 个 oauth 插件；CBAP 插件包还带 3 个：

| 通道 | 协议 | 关键点 |
|---|---|---|
| `dingtalk` 钉钉 | login.dingtalk.com oauth2 + api.dingtalk.com | 新版 OAuth2 回调参数是 `authCode`，接入层三种写法（code / auth_code / authCode）归一化；用户资料走 `x-acs-dingtalk-access-token` 头；unionId 透传 |
| `google` Google | accounts.google.com oauth2 | access_type=offline + prompt=consent 与参考插件一致；主键用 userinfo 的 `id` 而不是邮箱 |
| `qyweixin` 企业微信 | 服务商第三方应用（SuiteID + suite_ticket） | **需要「指令回调 URL」**：企业微信每 10 分钟推送 suite_ticket，本系统在 `/api/v1/auth/qyweixin/receive` 校验签名（sha1 排序拼接）、AES-256-CBC 解密后入库；登录回调再换 suite_access_token → `getuserinfo3rd`。没收到推送时明确提示去配置回调地址，而不是泛泛的「登录失败」 |

加解密实现口径与官方 PHP 示例逐项对齐（43 位 EncodingAESKey、IV=密钥前 16 字节、
PKCS7 块大小 32、明文结构 16 随机 + 4 长度 + msg + receiveid），并有
「密文往返 + 篡改签名拒绝」的单元测试钉死。

### 10.3 短信通道补齐（`internal/sms`）

§8 对齐了安装目录里的 6 个可用短信通道；CBAP 插件包还带 3 个明文插件：

| 通道 | 协议 | 关键点 |
|---|---|---|
| `officesms` 第二办公室 | POST open.2office.cn `Accounts/{account}/Sms/SendSms?sign=md5(account+authCode+timestamp)` | Authorization 头是 `大写(base64(account:timestamp))`；成功判据 `code=="0000000"`。参考实现有两处笔误：appId 取的是不存在的 `config["appid"]`（应为 account）；`processSendResult` 的状态赋值是无效表达式导致永远判失败——按正确语义实现并在测试里钉死 |
| `puddingv10sms` 布丁云 v10 | POST sms.idcbdy.cn/sendApi 表单 | `key=md5(secretKey)`；成功判据 `code==1` 是 PHP 松散比较，数字 1 与字符串 "1" 都算成功 |
| `tysms` 通用短信宝式 | GET `{url}?u=&p=md5(pass)&m=&c=` | 与短信宝同构的裸文本状态码；除常规错误码外，原实现的 `statusStr` 还挂着两个 uint64 溢出码（参数不全 / 服务器空间不支持），真实平台会返回，照收 |

三者的短信文案都走 `【签名】+ 模板渲染`（`content_template` 可选，占位符
`{code}`/`{ttl}`）；凭据字段（authCode / Secret Key / keySecret）继续加密入库，
后台「短信通道」面板按通道渲染对应表单。

### 10.4 人机验证通道补齐（`internal/captcha`）

魔方 `public/plugins/captcha/` 是「内置图形验证码 + 第三方通道」的结构。ShitIDC 原先
只有「有 Redis 就启用」的内置图形验证码；现在把第三方通道做成与短信/邮件同构的
注册表（`captcha_providers` 表 + 后台「人机验证」面板）：**启用中的通道优先，未配置
通道时原样回退内置图形验证码**，老部署行为不变。

| 通道 | 协议 | 关键点 |
|---|---|---|
| `google_captcha` 谷歌 reCAPTCHA | 前端注入脚本显式渲染 + 服务端 POST 校验 | 默认走 `www.recaptcha.net`（国内可直连）；请求体 `secret` / `response` / `remoteip`，`success===true` 才算通过；错误码翻成人话（超时 / 重复使用 / 域名不匹配等） |
| `tencent_captcha` 腾讯云验证码 | 前端 TCaptcha 弹窗 + 服务端 TC3-HMAC-SHA256 调用 `DescribeCaptchaResult` | 签名与短信同源，公共实现提到 `internal/tc3`（qcloudsms 改为复用，各自固定向量测试钉死签名）；判据 `CaptchaCode==1`，`Ticket` / `UserIp` / `Randstr` / `CaptchaAppId`（int64）/ `AppSecretKey` 一起提交 |

票据都是单次的：登录、注册、发送邮箱验证码、找回密码四个入口验票失败统一返回
`CAPTCHA_INVALID`，前端 `CaptchaInput.vue` 按 `provider` 重新出题（谷歌 `reset`、
腾讯重新弹窗、内置点图刷新）；组件通过 `/auth/captcha` 拿到公开参数
（`site_key` / `captcha_app_id`）或内置题的 `id` + `svg`。管理端字段元数据与
邮件/短信通道一致：`secret=true` 的字段（谷歌 SecretKey；腾讯 SecretID / SecretKey /
AppSecretKey）进加密凭据，其余进普通配置。

### 10.5 实名核验通道补齐（`internal/certification`）

§9.3 只对齐了安装目录里的 `alitwo`（云市场二要素）。CBAP 插件包与安装目录里还有 5 个实名认证插件，本轮全部补齐；扫码类通道引入了「初始化拿二维码 + 轮询」的第二条提交路径（此前只有同步核验一条）。

| 通道 | 协议 | 关键点 |
|---|---|---|
| `ali` 支付宝芝麻认证 | openapi.alipay.com，RSA2 签名 | initialize（拿 certify_id）→ 本地拼 page URL → query（passed 为 T/F，空=处理中）；响应对响应节点 JSON 原文做 RSA2 验签 |
| `idcsmartali` 智简魔方芝麻信用 | POST api1.idcsmart.com/certapi.php，头 api/key | initialize/certify/query 三动作；query 非 200 一律按处理中——参考实现把「未扫码」判成未通过，会把刚提交的用户直接写死失败 |
| `wechat` 微信人脸核身 | faceid.tencentcloudapi.com，TC3-HMAC-SHA256 | DetectAuth → BizToken/扫码地址（URL 做 htmlspecialchars_decode）→ GetDetectInfoEnhanced，Text.ErrCode==0 通过；查询遇 Error 节点同样按处理中
| `threehc` 银行卡要素 | GET {base}/cert/bank-card/{type}，APPCODE | type=2/3/4 对应二/三/四要素；number 传身份证、bank 传银行卡；判据 ret==200 且 data.desc 为「一致」 |
| `phonethree` 手机三要素 | GET {url}?idcard&phone&realname，APPCODE | code==200 一致；ordersign 作为凭证附加在结果里 |

扫码流程：提交命中 `Challenger` 接口时不再同步核验，而是落一条 pending 记录（provider_ref / provider_url 存凭证与二维码地址，不对外返回），返回 {status:"pending", url}；前端 Profile.vue 用 NQrCode 渲染二维码并每 3 秒轮询 GET /api/v1/certification/poll，命中后由 ResolveCertification 写成终态（只动 pending，管理员已处理的记录不会被后到的轮询改写）。

迁移：migrations/026_certification_provider_ref.sql（certifications 表加 provider_ref / provider_url 两列，默认空串，存量记录不受影响）。 |

### 10.6 实名核验通道补齐（二）：涪擎与 E证通（本轮补齐）

§10.5 之后复查 CBAP 插件包，certification 类目还剩两个明文插件，补齐后该类目可读插件全部对齐：

| 通道 | 协议 | 关键点 |
|---|---|---|
| `fuplusx` 涪擎实名认证（高级版） | GET {base}{path}，APPCODE；默认 fephone.market.alicloudapi.com | 一个通道按 type 覆盖二/三/四要素：/IDCard、/bankCheck、/phoneCheck、/bankCheck4；判据 status=="01"，traceId 作为凭证；参考实现的 type=4 不收集手机号（从认证记录里取），本系统没有这条来源，type=4 一并把手机号声明为必填扩展字段 |
| `yerzt` E证通人脸认证 | faceid.tencentcloudapi.com，TC3-HMAC-SHA256 | GetEidToken → EidToken/扫码地址（Url 做 htmlspecialchars_decode）→ CheckEidTokenStatus（timeout 保持处理中）→ GetEidResult，Text.ErrCode==0 通过；与插件的差异：插件把 Text.ErrCode!=0 也当「尚未通过」无限轮询，本实现按已出结果判失败，用户可直接重新提交；input_type 缺省 3（提交前已收集姓名与证件号，扫码端无需 OCR） |

两个通道复用既有基础设施（云市场错误翻译 / internal/tc3），后台「实名核验通道」面板按通道渲染对应字段。至此魔方参考源 certification 类目里协议可读的插件（alitwo / ali / idcsmartali / wechat / threehc / phonethree / fuplusx / yerzt）全部在 ShitIDC 有对应的 Go 实现。

### 10.7 支付网关补齐：支付宝国际支付（global_alipay）与 Stripe 加固（本轮补齐）

CBAP 包 gateway 目录的明文插件此前大多已对齐，本轮清掉最后两个：

| 通道 | 协议 | 关键点 |
|---|---|---|
| global_alipay 支付宝国际支付（境外收单） | GET https://intlmapi.alipay.com/gateway.do，service=create_forex_trade（wap 用 create_forex_trade_wap），MD5 签名 | 参数去掉 sign/sign_type/空值后按名排序拼 k=v&k=v 再直接拼密钥；total_fee = 金额 × rate（bcmul 截断到分），回调按 total_fee ÷ rate 还原（本实现四舍五入，避免 bcdiv 截断的 ±1 分漂移）；trade_information={"business_type":5,...} 标记服务费类交易；「服务费」文案按插件替换为 " Service Fee"；差异：回调增加 trade_status ∈ {TRADE_SUCCESS, TRADE_FINISHED} 校验（插件只验签名，WAIT_BUYER_PAY 也会被当成成功） |
| stripe Stripe 加固 | Checkout Session + webhook | 按参考插件 stripe 的判据补 payment_status=="paid" 校验：checkout.session.completed 在异步支付方式下可能带 unpaid，只认 paid；签名/事件类型/时间窗/Refund 等既有逻辑不变 |

凭据形态：global_alipay 的 Secret 为 JSON {"key","currency","rate"}（currency 默认 HKD，rate 默认 1，rate = 收取货币/系统货币），也接受纯密钥字符串；后台「支付渠道」面板已加中文标签。

### 10.8 附属插件补齐：到期 IP 记录与数据导出至 Excel（本轮补齐）

主程序包 public/plugins/addons/ 里除加密插件（expired_auto_delete_bill / product_divert）与示例插件（demo_style）外，还剩两个可读插件，本轮对齐：

| 插件 | 参考实现 | ShitIDC 对齐 |
|---|---|---|
| expired_ip_log 到期产品删除IP记录 | 挂 afterModuleTerminate，把 host 的 dedicatedip / assignedips / regdate / uid 写入 shd_expired_ip_log，后台列表查看 | 新增 expired_ip_logs 表；worker 终止成功（FinalizeServiceTransition 之后）调用 RecordExpiredIPLog 快照 IP——IP 取自开通时写入 provider_payload 的实例数据（NOKVM 的 main_ip / assigned_ips，另按 dedicatedip / server_ip / ip 等别名兜底）；后台「到期IP记录」页可搜索，记录只增不改 |
| export_excel 数据导出至 Excel | shd_export_plugin 保存「自定义名称 + 导出列表 + 参数字段」，PhpSpreadsheet 按时间区间导出；内置 billPay（账单列表（已支付））与 achievement（我的业绩） | 新增 export_configs 表与「导出中心」页：自定义名称 + 数据集 + 字段多选，按收款/结算时间区间导出 xlsx；internal/xlsx 用 archive/zip 按 OOXML 最小结构生成（内联字符串 + 加粗表头 + 金额数字列），不引第三方库。数据集：bill_pay「账单列表（已支付）」（含付款方式 / 在线支付金额 / 余额拆分，主机与主 IP 取订单开通的第一个服务）与 achievement「我的业绩」——ShitIDC 没有「业务经理」，业绩按推广人佣金口径，字段差异已在此说明 |

迁移：027（expired_ip_logs）/ 028（export_configs，含两条预置列表）。


### 10.9 线下支付渠道（user_custom 插件对齐）

参考插件 gateways/user_custom 是「人工收款」模型：`UserCustomHandle` 从渠道配置读出收款说明（HTML），下单接口返回 `type=html`，主题端 `$('#pay-type .add-html').html(addHtml)` 原样注入展示；插件没有异步回调，是否到账完全靠人工判断。ShitIDC 按同一模型落地为 manual 渠道：

| 环节 | 魔方 user_custom | ShitIDC manual |
|---|---|---|
| 渠道配置 | seller_id 字段存收款说明 HTML | 后台「支付渠道」新增 manual 渠道：config.message 存收款说明（支持 HTML），pay_types 固定为 ["manual"]，无需填写商户密钥 |
| 下单 | 返回 type=html / addHtml，前端原样注入 | /orders/:id/pay/online 返回 {html, out_trade_no, need_confirm:true}，订单页弹窗展示收款说明与订单号；返回前确保存在 method=manual 的待支付登记单（复用或补建） |
| 收款确认 | 无回调，人工在后台标记 / 开通 | 管理端 POST /admin/orders/:id/confirm-payment（wallet.adjust + CSRF）：校验金额后走 CompleteOnlinePayment，成功后复用在线支付的同一后处理（审计 / 推广佣金 / 开通入队 / OrderPaid / InvoicePaid / WalletRecharged） |
| 钱包充值 | 插件未特别限制 | 明确不支持：手动建单与 PrepareRecharge 都拒绝线下支付，避免「转账后余额无人确认到账」 |

无新增迁移（复用 payment_providers 的 config JSON 字段）。前端：订单页「选择支付方式」按渠道切换文案与弹窗，钱包充值选项过滤 manual，后台可配置收款说明并在订单中心「确认收款」。

### 10.10 待办事项 widget（widget/ToDo）与实名审核页（本轮补齐）

CBAP 包 `widget/` 下只有一个插件 `ToDo`：管理端首页把各附属插件的待处理数量聚合成一张卡片，装了哪个插件就显示哪一项，点击跳转到对应插件页（`sub_server/` 的 bthostx/kanghostx 已补齐，见 §10.11/§10.12；`template/` 的 cart1/cart2 与 Mf101 见 §10.13）。ShitIDC 按同样口径落地为管理控制台「待办事项」面板（`GET /admin/todos`）：

| 魔方 ToDo 项 | ShitIDC 数据口径 | 落地页 |
|---|---|---|
| pending_work_orders 待处理工单 | tickets status IN ('open','pending') | /admin/tickets |
| pending_real_name_authentication 实名认证待审 | certifications status='pending' 且 provider_url 为空（扫码轮询中的记录会自动出结果，不计入） | /admin/certifications（本轮新增） |
| pending_host_num 开通中产品数量 | services status IN ('pending','provisioning') | /admin/services |

未映射项（均为业务模型本身不同，不强行编造）：
- pending_refunds 待处理退款：ShitIDC 退款是即时的冲正交易（钱包入账）或网关原路退回，没有「待处理」队列；
- pending_withdrawals 待处理提现、to_be_confirmed_recommend 待确认推介：ShitIDC 无提现功能，推广佣金为支付成功即时入账；
- pending_invoices 待处理发票：魔方 IdcsmartInvoice 指发票（开票 / 寄送）流程，ShitIDC 的 invoice 是账单（unpaid/paid/void），语义不同（开票申请流程已于 §10.24 对齐，widget 计数仍按账单口径）。

与插件的差异：魔方 ToDo 对所有管理员显示同等项；ShitIDC 的 /admin/todos 按模块权限过滤——ticket.manage / user.manage / service.manage 各见各的项，三项权限都没有返回 403，前端同样按权限渲染。

顺带补齐实名审核界面：`adminListCertifications` / `adminReviewCertification` 两个 API 在引入实名核验通道时就已存在，但一直没有管理端入口。本轮新增「实名审核」页（状态筛选、通过、驳回并写明原因；记录始终脱敏展示），接入后台侧边栏，也作为待办事项里「待审实名认证」的落地页。

### 10.11 kanghostx（V10 kangle 模块）配置键位对齐

CBAP 包 `sub_server/kanghostx` 是「Kangle对接模块（V10版）」：面板侧协议与随包发布的 `servers/wlkanglepro` 完全同构（同一套 `s = md5(a + token + r)` 签名与 `/api/index.php?c=whm` 动作集），差异集中在商品配置键位与带宽单位上。本轮不新起第二个 Provider，而是把该键位直接接进现有 `wlkangle`（`internal/provider/wlkangle/wlkangle.go`）：两套键位互不重叠（wlkanglepro 用 `type`/`web_quota`，kanghostx 用 `way`/`parameterN`/`kl_*`），命中 kanghostx 键位时走 `kanghostxForm`，其余行为不变。

| 维度 | kanghostx（V10） | ShitIDC 落地 |
|---|---|---|
| 开通方式 | `way`：0 自定义 / 1 弹性 | `isKanghostxConfig` 识别（way 或 parameter*/kl_* 键）；`way` 取 1/true/是 时读弹性键位 |
| 自定义参数 | `parameter1..16` + `ftp` | 逐项映射 add_vh：cdn / subdir_flag / domain / max_subdir / subdir / web_quota / db_quota / flow_limit / speed_limit / max_connect / access / log_file / log_handle / ssi / htaccess / port |
| 弹性参数 | `kl_site` `kl_sql` `kl_domain` `kl_zi` `kl_flow` `kl_speed` `kl_connect` `kl_access` `kl_htaccess` `kl_log_file` `kl_log_handle` `kl_ssi` | 同键位映射；`parameter1/2/5/16` 与 `ftp` 两种方式共用 |
| 带宽单位 | 配置项是 M，PHP 里 `值 * 128` 换成面板的 KB | `speedLimitKb` 复刻 ×128；只作用于 kanghostx 键位（wlkanglepro 的 speed_limit 本就是 KB，不换算） |
| 改配 | `_ChangePackage`：`add_vh&init=1&edit=1` | 同一映射 + `edit=1`（passwd 留空沿用现有密码，与既有实现一致） |
| 签名 / 动作 | md5concat（`a`/`r`/`s` + `json=1`） | 复用既有客户端，签名与动作集完全一致 |

有意差异（2 处，均为修正参考实现的问题）：

- 参考实现 `_ChangePackage` 不区分 `way`、固定读 `kl_*`；ShitIDC 跟随 `way` 读对应键位，避免自定义产品（way=0）升降级时丢参数。
- 空值不下发的口径与 PHP 的 `isset && !empty` 对齐，但「0」PHP 的 `!empty('0')` 会把它当空丢掉（如 `db_quota=0` 表示不开通数据库）；ShitIDC 按配置项语义原样下发。

未落地项（接口/形态没有对应物，不强行编造）：

- `GetHostInfo`/`Status`（`getvh`：0 运行 / 1 暂停 / 2 超流量 / 3 超数据库）：Provider 接口没有状态同步动作，也没有消费入口（服务状态由 ShitIDC 自己的 services 表管理）；
- `on`/`off`（开/关机前查状态、超流量/超数据库拒绝操作）：这是面板管理端的独立动作对；PHP 的 `_SuspendAccount`/`_UnsuspendAccount`（以及 `_Renew`）本身没有状态守卫，ShitIDC 维持同一语义；
- `ClientArea`/`ClientAreaOutput`（主机信息表、面板登录表单）与 `getServerIp`、`AllowFunction`：PHP 平台侧模板与能力声明；SPA 下对应信息在开通时落库（实例 Data 里的面板地址/账号/密码）。

导入侧：`internal/zjmfimport` 按「形态」识别签名（`_CreateSign` 出现 `md5(` 即 md5concat），kanghostx 与 wlkanglepro 同构，其 `_ConfigOptions`（way/parameterN/kl_*）可被解析成商品配置项，配合本次键位映射直接可用。

### 10.12 bthosts（btHost 虚拟主机）Provider（本轮补齐）

魔方随包发布的 `servers/bthosts`（btHost 对接模块，APIVersion 1.7.1，明文）与 CBAP 包 `sub_server/bthostx`（宝塔虚拟主机 Bthost 模块 V10 版）对接的是同一套上游 `/api/vhost/*` API。本轮新增原生 Provider `internal/provider/bthosts`，两套商品配置键位都支持。

| 维度 | 参考实现 | ShitIDC 落地 |
|---|---|---|
| 签名 | `strtoupper(md5(implode(sort([time, random, accesshash]))))`，token 只参与摘要、不随请求发送 | `sign`；`time/random/signature` 随表单（POST）或查询（GET）下发 |
| 探活 | `TestLink`：GET `/api/vhost/index` | `TestConnection` 同 URL，`code == 1` 判据 |
| 开通 | `user_create`（username + password）取 `data.id`，再 `host_build`（`pack[...]`）取 `data.site.id` | `Create` 两步同序；用户名取开通请求 ID 的字母数字片段（≤32），随机 10 位密码 |
| 暂停 / 启用 / 删除 | `host_locked` / `host_start` / `host_recycle`（进上游回收站） | 同名三动作 |
| 续费 | `host_recovery` → `host_start` → `host_endtime`（`Y-m-d`） | 同序列；前两步尽力而为，`host_endtime` 失败返回错误（见下文差异） |
| 升降级 | 套餐走 `host_update(plan_id)`；自定义/弹性走 `host_edit` + 限速联动 | `ChangePackage` 同口径；`applySpeed` 复刻 `_Speed/_UnSpeed`：并发或带宽为 0 → `host_speedoff`，否则 `host_speed` |
| 经典键位 | 1.7.1：`type`（0 自定义 / 1 套餐 / 2 弹性）、`plans_id`、`sort_id`、`port`、`domain_num`、`web_back_num`、`sql_back_num`、`domainpools_id`、`ippools_id`、`ip_num`、`phpver`、`perserver`、`limit_rate`、`site_max`、`sql_max`、`flow_max`、`sub_bind` | 逐键映射 `pack[...]`，原值直达上游；`type == 1` 带 `plans_id` 与 `sort_id` |
| V10 键位 | 自定义 `way=0` 读 `parameter1..20`；弹性 `way=1` 读 `bt_site/sql/domain/flow/webback/sqlback/ipnum/perserver/limit` 与共用 `parameter1..11` | `isV10` / `isElastic` 分流后逐键映射，与参考实现一致（session 按服务端读取的 `parameter4`；参考实现的 ConfigOptions 把该项 key 误写成 `parameter1`） |
| 单位换算 | `parameter14`/`bt_flow`（G）×1024 → `flow_max`（MB）；`parameter15`/`bt_limit`（MB/s）×1024/8 → `limit_rate`（KB/s） | `scaleInt` 同系数；经典键位不换算，原值直达 |

有意差异：

- `Renew` 里 `host_endtime` 失败返回错误：参考实现 `_Renew` 把失败也包成 `status=success`（msg 写「续费失败：…」），调度层会误判成功；`host_recovery`/`host_start` 与参考一致保持尽力而为、不阻断。
- 开通时的 `endtime`：经典的 `_CreateAccount` 用 `nextduedate`，但 Provider 的开通请求没有交期（只有 `RenewRequest` 带 `ExpiresAt`），故开通按 V10 的兜底值 `2099-12-31`，续费时由 `Renew` 同步真实到期时间。
- 实例数据额外写入 `dedicatedip`（面板主机名），对齐参考实现开通成功后写 `dedicatedip`（服务器 host）的行为，供「到期产品删除 IP 记录」使用。
- 用户名生成：经典用域名、V10 用主机名；Provider 两个字段都拿不到，取开通请求 ID 的字母数字片段（同 wlkangle/nokvm 口径）。

未落地项（接口/形态没有对应物，不强行编造）：

- `host_info`/`host_status`（状态）、`host_sync` + `user_info`（同步）、`host_pass`（改密）、`host_stop`、`host_resource`（用量）：Provider 接口没有对应动作或消费入口；
- 绑定/解绑域名等上游用户面板功能与 `ClientArea`/`ClientAreaOutput`：属平台模板/面板侧能力，无对应物。

验证：`gofmt` / `go build ./...` / `go vet` / `vue-tsc --noEmit`。

> 备注：仓库既有的 `baota` Provider 面向宝塔面板的站点接口（`sites?action=*`），与本轮的 `/api/vhost/*` 不是同一上游，两者并存。

### 10.13 template 插件（cart1/cart2 购物车皮肤、Mf101 官网模板）（本轮说明）

CBAP 包 `template/` 下是上游的 **PHP 前台皮肤**，不是可移植的服务端模块：

- `cart1.zip` / `cart2.zip`：PC 购物车主题皮肤（`cart/template/pc/cart1|c2`，含 goods / goodsList / shoppingCar / settlement / goods_iframe 页面与配套 js/css），两套几乎相同、仅样式有差异；
- `Mf101.zip`：整套静态官网模板（226 个文件，`web/mf101` 下 html 与 assets）。

皮肤形态与本站 SPA 不同，不逐文件移植；对齐的是它承载的功能面——购物车与结算。其调用的后端能力（购物车、优惠码、等级优惠、活动促销、结算付款）此前已在 §2.89 就绪，本轮缺的只是前台界面，现已补齐（提交 `8afbc79`、`63eb29f`）：

- 新增 `web/src/views/Cart.vue`：改数量 / 移除 / 清空 / 优惠码 / 结算弹窗；余额可整批一次支付，余额不足引导到钱包；
- 路由 `/cart` + 顶栏「购物车」入口 + 商品页购买弹窗双按钮（「加入购物车」/「创建订单」）；
- 后端加购支持指定币种（`AddCartItemWithCurrency`）：`currency` 非空时按币种精确匹配 `product_prices`，为空回退首个可用币种——多币种商品在购物车里保持用户选中的币种。

购物车口径与 §2.89 一致：不存价格（每次读取重算）、结算只锁价不收钱、一个结算批一次付清、付款带幂等键。

有意差异 / 未落地：

- 皮肤里嵌的域名注册行（`idcsmart_domain` 的 domain_suffix / check_domain / get_price / whois / 信息模板 / 批量查询）与 `e_contract` 电子合同（EContract 插件加密）不迁移——对应插件不在本轮范围（域名类见 §9.4 明确跳过）；
- 皮肤自带的菜单 / 合同类前端接口同属上述插件，一并跳过；
- Mf101 属浏览式静态官网模板，与 SPA 形态不同，不搬运（官网职能由 SPA 承担）。

导入侧说明：`template/` 与 `internal/zjmfimport`（魔方 server 模块导入器）无交集，导入流程不受影响。

验证：两笔提交的静态检查均通过（`gofmt` / `go build ./...` / `go vet` / `vue-tsc --noEmit`）。

### 10.14 oss 插件（TencentcloudOss 对象存储）（本轮补齐）

CBAP 包 `oss/TencentcloudOss.zip` 是明文插件（module=oss，主类 `TencentcloudOss`），对接腾讯云 COS；魔方主程序加密，只有插件侧契约可读。本轮新增原生通道 `internal/oss`（通道注册表 + `oss_providers` 表 + 后台面板），并把工单附件接上：配置通道后上传转存对象存储、下载 302 到 3 分钟签名地址；未配置时保持本机存储，默认行为不变。

| 维度 | 参考实现 | ShitIDC 落地 |
|---|---|---|
| 配置 | `module_name` / `secret_id` / `secert_key`（原版拼写）/ `bucket` / `region` | 通道名 + bucket/region（公开）+ secret_id/secret_key（加密凭据；按正确拼写存储） |
| 探活 Link | 官方 SDK `HeadBucket` | `TestLink`：HEAD 桶地址，复用同一套签名器 |
| 上传 Upload | `putObject`：图片 / PDF 用 `public-read`，其余 `private`；Key=file_path+file_name 去掉 WEB_ROOT | `Upload`：同口径 ACL 规则；工单附件 Key=`uploads/tickets/{工单ID}/{uuid}{ext}`，转存成功后删除本机文件 |
| 列表 Data | `listObjects` | 未落地（站内无列举入口；对象存在性用 `Exists`=HEAD 对象） |
| 下载 Download | `getObjectUrl` 签名地址，有效期 +3 分钟 | `SignedURL`：COS v5 签名，默认 3 分钟；工单附件下载 302 跳转 |
| 签名 | SDK 内部 COS v5 | 标准库实现 v5（KeyTime / SignKey / HttpString / StringToSign / Signature），路径参数按 COS 规则编码（空格 %20、保留 /） |

有意差异 / 未落地：

- 不引入官方 SDK（仓库惯例：第三方对接标准库实现）；
- 不做 `listObjects` 全量列举；探活用 HEAD 桶、下载只按需 HEAD 对象（O(1)，大桶友好）；
- 参考实现拼写 `secert_key` 属笔误，落地用 `secret_key`（新表新数据，不影响存量）；
- 下载 302 后由 COS 直接响应对象，无法再强制 attachment 响应头；上传类型白名单本就不含 HTML / SVG，风险面不变。

验证：`gofmt` / `go build ./...` / `go vet` / `vue-tsc --noEmit`。

### 10.15 CBAP addon/ 30 个插件包审计（本轮说明）

CBAP 仓库 `plugins/addon/` 下 30 个 zip 经逐个检查：**包内全部 `.php`（含 `config.php` 与语言包）均为 ionCube 编码**，主类与接口契约不可读；可读的只有前端资产（`template/**` 的 js/html/tpl、邮件模板 html、vendor 目录、`menu.php` 与 16 字节 README 等）。没有可读契约就不编造行为，本轮给出逐包审计与去向：

| 包 | 前端可读线索 | 判定 |
|---|---|---|
| AbnormalInspectionRecords | 异常记录：关联产品、标签、导出 | 已对齐（§10.18） |
| ClientCare | 客户关怀：邮件/站内信、周期推送、指定用户 | 已对齐（§10.28） |
| ClientCustomField | 客户自定义字段（管理列表/申请） | 已对齐（§10.19） |
| CostPay | 支出记录：来源/主体/金额/日期 | 已对齐（§10.17） |
| CreditLimit | 授信：消费记录、混合支付 | 已对齐（授信账户 + 后台授信管理） |
| CycleArtificialOrder | 人工订单：调价、批量、子项调价 | 已对齐（§10.23） |
| EContract | 电子合同：模板/签署/邮寄 | 已对齐（§10.36） |
| EmailNoticeAdmin | 管理员邮件通知：接口+模板+收件人 | 已对齐（§10.25） |
| EventPromotion | 促销：满减/百分比、时间窗 | 已对齐（§10.22） |
| FlowPacket | 流量包管理 | 已对齐（§10.27） |
| HostTransfer | 主机转移 | 已对齐（§10.26） |
| IdcsmartClientLevel | 客户等级：三级、商品可选、批量保存 | 已对齐（客户组差异定价，口径等价） |
| IdcsmartDomain | 域名 | 跳过（§9.4） |
| IdcsmartInvoice | 开票申请：抬头/快递/邮寄/驳回 | 已对齐（§10.24） |
| IdcsmartRecommend | 推介计划：奖励记录、奖励比例、提现 | 已对齐（§10.31） |
| IdcsmartSale | 销售统计：消费排名、时间窗图表 | 已对齐（业务经理维度，§10.35） |
| IdcsmartStatistics | 统计图表 | 已对齐（后台统计/仪表盘） |
| IdcsmartVoucher | 代金券：发放/使用/次数 | 已对齐（§10.21） |
| IdcsmartWebhook | 消息推送（钉钉/企业微信等） | 已对齐（internal/webhook + 后台 Webhook 页） |
| ManualResource | 手动资源：供应商、noVNC 控制台 | 已对齐（§10.34） |
| NoticeSendMerge | 通知合并发送 | 跳过（包内无任何可读契约，README 为「插件样式Demo」，§10.37） |
| ProductCashback | 商品返现 | 已对齐（§10.16） |
| ProductCertLimit | 产品实名限制 | 已对齐（§10.20） |
| ProductCycleLimit | 购买周期限制 | 已对齐（§10.20） |
| ProductDropDownSelect | 商品下拉选择 | 已对齐（§10.32） |
| ProductNumLimit | 购买数量限制 | 已对齐（商品自带单客户限购） |
| ProductRelatedLimit | 关联购买限制 | 已对齐（§10.20） |
| TicketInternalPremium | 工单内部备注/内部工单 | 已对齐（§10.29） |
| TicketPremium | 工单高级版（部门/字段/回执模板） | 已对齐（§10.30） |
| WanyunResource | 万云资源：自定义字段、节点 | 已对齐（§10.33） |

主程序包 `zjmf-finance/public/plugins/addons/` 的 5 个（demo_style 示例、expired_ip_log / export_excel 已对齐见 §10.8、product_divert 已按前端契约重新落地见 §10.38、expired_auto_delete_bill 已按前端契约重新落地见 §10.39）全部收口。

说明：本表 30 个插件现已全部对齐或有明确跳过结论——可读契约的（含最初判定「未落地」的 ManualResource / WanyunResource / IdcsmartSale / EContract / ProductDropDownSelect）均按前端资产可见的字段面直接设计实现（§10.31 起的思路）；NoticeSendMerge 连前端资产都没有（§10.37），EContract 的第三方电子签通道以站内流程等价替代（§10.36）。

验证：本轮纯审计与文档，无代码改动。

### 10.16 ProductCashback 插件（商品返现）（本轮补齐）

CBAP 包 `addon/ProductCashback.zip` 主类加密，但 `template/admin/api/index.js` 与语言包可读，契约完整：后台按商品配置返现规则（`product_id`、`type=fixed`、`price`、`period`、`status`），列表 / 新增 / 编辑 / 启停 / 删除五个接口 + 商品选择。本轮按该契约落地「商品返现」：

| 维度 | 参考实现 | ShitIDC 落地 |
|---|---|---|
| 规则字段 | product_id / type（固定金额）/ price / period / status | `product_cashbacks` 表（030 迁移）：product_id 唯一、price_cents、period_days、active |
| 后台接口 | GET/POST /product_cashback、PUT /product_cashback/{id}、PUT /{id}/status、DELETE /{id} | `/admin/product-cashbacks` 五件套（`product.write` 权限 + CSRF + 审计） |
| 后台页面 | 插件自带管理页 | `/admin/cashbacks`「商品返现」页：商品选择、金额（元）、期限、启停、编辑、删除 |
| 返现行为 | 购买后返现到账户余额；返现金额超过购买金额时按购买金额返现 | 支付成功收尾（afterPaymentCompleted）调用 `PayProductCashback`：金额 = min(规则金额, 订单项小计)，多商品合并入账 |
| 幂等 | 插件未说明 | 钱包流水 `idempotency_key=cashback:{订单}`，同一订单只返一次；币种随订单 |

口径说明（加密代码无法验证，按可见契约的最保守解释实现）：

- 「可返现期限」= 购买后 N 天内（0=永久）：超期订单不返现；页面按「购买后 N 天内 / 永久」展示；
- 返现即时到账（支付成功即入账），钱包流水 reference_type=cashback，用户账单页可见「商品返现」入账记录；
- 多币种：按订单币种等额入账（原插件为单币种系统，无汇率换算语义）；该币种无钱包账户时跳过、不影响支付。

验证：`gofmt` / `go build ./...` / `go vet` / `vue-tsc --noEmit`。

### 10.17 CostPay 插件（成本支出）（本轮补齐）

CBAP 包 `addon/CostPay.zip` 主类（`controller/model/validate` 与语言包）为 ionCube 加密，但前端资产完全可读：`template/admin/js/order_cost.js`、`template/admin/api/client.js`、`template/admin/lang.js`、`order_cost.html`，接口契约、字段、权限点与排序语义均取自这些文件。

| 维度 | 参考实现（插件前端契约） | ShitIDC 落地 |
|---|---|---|
| 支出字段 | name 支出名称、owner 所属主体、cost 支出金额、cost_time 支出日期（Unix 秒）、notes 备注（≤200 字）、self_defined_field 字典 | `order_cost_pays` 表（031 迁移）：name/owner/cost_cents/cost_time/notes + `order_cost_pay_values` 存字段值；金额落「分」 |
| 支出接口 | GET/POST `/order/{id}/cost_pay`、GET/PUT/DELETE `/cost_pay/{id}` | `/admin/orders/:id/cost-pay`、`/admin/cost-pay/:id`（finance.report + CSRF + 审计） |
| 列表筛选 | page/limit、keywords（名称/备注）、owner、支出日期区间、最近记录时间区间 | 同名参数：keywords/owner/start_cost_time/end_cost_time/start_create_time/end_create_time（Unix 秒或 RFC3339） |
| 列表返回 | list、count、owner（主体候选）、self_defined_field（字段数组） | 同构返回，另附 order 摘要；每条记录带 `self_defined_field` 值字典（键为字段 id） |
| 自定义字段 | field_name、field_type（text/dropdown）、is_required、field_option（英文逗号分隔）、show_list 列表展示开关、拖动排序（`prev_id`，0=最前） | `/admin/cost-pay/self-defined-field` 五件套 + `/show-list` + `/drag`；排序服务端按「移到 prev 之后」重排权重 |
| 记录人 | create_time 列展示 admin_name | admin_id 关联 users，列表返回 admin_name；创建/修改/删除写审计日志 |
| 看板 widget | AddonCostPayToday / ThisMonth / ThisYear / ThisYearCost（加密不可读，按名称口径） | `/admin/cost-pay/summary` 按币种汇总今日/本月/今年支出，页面「成本支出」顶部展示 |
| 后台页面 | 插件挂在订单详情 Tab 内 | `/admin/order-costs?order_id=`：订单摘要 + 支出列表（动态字段列）+ 新增/编辑弹窗 + 字段管理弹窗；订单中心每行「成本支出」入口 |

口径说明：

- 金额：插件前端以「元」小数提交与展示，本站库内与接口均用「分」整数（`cost_cents`），页面换算展示；接口同时兼容 `cost`（元，小数）入参，方便按插件契约直连；
- 支出日期为必填；列表按支出日期倒序；自定义字段的「必填」在服务端强制校验，下拉字段值必须在选项内（加密的 Validate 类无法比对，按最保守解释实现）；
- 字段排序：插件用前端拖动 + `prev_id`；页面提供「上移/下移」按钮，调用同一 `/drag` 语义（移到目标前一位之后），服务端重排全量权重，避免权重碰撞；
- 时间口径（今日/本月/今年）沿用站内统计约定：PostgreSQL `date_trunc` 取数据库时区（UTC）；
- 插件权限点（auth_addon_cost_pay_show_tab/create/update/delete/field_manage）对应站内统一权限 `finance.report`（成本属财务敏感数据，与「财务统计」「导出中心」同权限）。

验证：`gofmt` / `go build ./...` / `go vet` / `vue-tsc --noEmit`。

### 10.18 AbnormalInspectionRecords 插件（异常巡查记录）（本轮补齐）

CBAP 包 `addon/AbnormalInspectionRecords.zip` 的 PHP（含 config/lang/route）全部 ionCube 加密，前端 `template/admin/api/index.js`、`lang/zh-cn.js`、`js/index.js`、`index.html` 可读，接口与字段面完整取自这些文件。

| 维度 | 参考实现（插件前端契约） | ShitIDC 落地 |
|---|---|---|
| 记录字段 | client_id 异常用户、host_id 关联产品、ip 异常时IP（必填、格式校验）、matter 异常事项、measure 处理措施、process_time 处理时间、img 异常截图（多张） | `abnormal_inspection_records` 表（032 迁移）：user_id/service_id/ip/matter/measure/process_time/images(JSONB) |
| 展示字段 | username/company/phone/email、product_name/host_name、order_id、pay_time、admin_name | 列表 join users / user_profiles / services / products / orders 实时读出；pay_time 取订单 `paid_at`；admin_name 取最后提交人 |
| 接口 | GET/POST /abnormal_inspection_records、PUT/DELETE /{id}、GET /export_excel（blob） | 同名语义：`/admin/abnormal-inspection-records` 增删改查 + `export.xlsx` + 截图上传 / 读取（service.manage + CSRF + 审计） |
| 列表参数 | keywords（用户/公司/联系方式/IP）、start_time/end_time、page/limit/orderby/sort | 同名参数；时间过滤按「处理时间」，输出时间字段一律 Unix 秒 |
| 截图 | 上传后存 save_name，展示时可放大（viewer） | `POST /admin/abnormal-inspection-records/images`（png/jpg/jpeg/webp/gif、≤5MB、magic 校验）→ 本机 `uploads/inspection/`；配置了对象存储通道则转存 OSS（`oss:` 前缀，读取 302 签名地址）；`NImage` 点击放大 |
| 权限 | auth_abnormal_inspection_records_{create,check,update,delete,export}_record | 统一 `service.manage`（巡查是服务运维工具） |
| 后台页面 | 插件自带管理页 | `/admin/inspection-records`：关键词 / 处理时间筛选、列表、新增 / 编辑（用户 → 其名下产品联动选择）、详情弹窗（含截图）、导出 Excel |

口径说明（加密代码无法比对，按可见契约的最保守解释）：

- 列表日期区间按「处理时间」过滤（插件参数名 start_time/end_time 无更多线索；处理时间是该记录唯一的业务时间字段，购买时间是订单派生字段不可编辑）；
- 记录关联的是「用户 + 其名下的服务（产品）」，服务必须属于所选用户；订单 ID 与购买时间由服务对应订单实时派生，不冗余落库（插件为冗余存储，资料会过期）；
- 站点用户资料无「国家码」分列，`phone_code` 固定返回空串，页面直接展示 `phone`；
- 截图删除记录时不删文件（与插件一致，保留回溯材料）；`img` 入参同时兼容插件的 `["文件名"]` 与本站 `[{stored,name}]` 两种形状。

验证：`gofmt` / `go build ./...` / `go vet` / `vue-tsc --noEmit`。

### 10.19 ClientCustomField 插件（客户自定义字段）（本轮补齐）

CBAP 包 `addon/ClientCustomField.zip` 的 PHP（controller/model/validate/route）全部 ionCube 加密，前端 `template/admin/api/index.js`、`js/index.js`、`lang/zh-cn.js` 可读，接口与字段面取自这些文件。

| 维度 | 参考实现（插件前端契约） | ShitIDC 落地 |
|---|---|---|
| 字段定义 | name、type（text/dropdown/link/password/tickbox/textarea/dropdown_text）、options（英文半角逗号分隔）、description、regexpr、admin_only、required、before_settle、show_register、status、拖拽排序 | `client_custom_fields` 表（033 迁移）同名字段；`client_custom_field_values` 存用户值（field_id+user_id 唯一） |
| 后台接口 | GET/POST /client_custom_field、PUT /{id}、PUT /{id}/status、DELETE /{id}、PUT /{id}/drag（prev_id，0=最前） | `/admin/client-custom-fields` 同名语义（user.read 读 / user.manage 写 + CSRF + 审计） |
| 用户值接口 | GET /client/{id}/client_custom_field_value（管理员看某用户的字段与值） | `GET /admin/users/:id/custom-fields`（含管理员专用字段及 has_value） |
| 用户侧 | clientarea 控制器（加密）：个人中心填写、注册时显示的可提交 | `GET/PUT /profile/custom-fields`（password 不回显、留空=不修改）、`GET /auth/register-fields`（公开只读，注册页动态渲染）、注册接口接受 custom_fields |
| 门禁 | required / before_settle（订购前必填） | 服务端强制校验（必填 / 下拉选项 / 正则 / 长度）：保存与注册均校验；checkout 缺 before_settle 字段返回 409 CUSTOM_FIELD_REQUIRED |
| 排序 | 前端拖动 + prev_id | 同一 `/drag` 语义，服务端整表重排权重；页面提供上移 / 下移按钮 |
| 后台页面 | 插件自带管理页（keywords 搜索 + 分页） | `/admin/client-fields`：列表（类型 / 描述 / 可见位置 / 订购前必填 / 显示状态）、新增 / 编辑（类型不可改）、状态开关、删除确认（有数据显示条数） |

口径说明（加密代码无法比对，按可见契约的最保守解释）：

- 类型共 7 种与插件一致；tickbox 与插件前端一致不提供「必填 / 订购前必填」；
- 删除字段连带删除字段值（插件提示「当前字段可能存在数据,是否确认删除?」，页面按 value_count 提示条数）；
- password 读取只回传 has_value（是否已设置），保存空值保持原值；其余类型未提供的字段视为清空；
- 管理员专用字段不参与用户自助填写与注册，也不在 checkout 门禁范围内；
- 插件后台列表支持 keywords / 分页，本站字段数量少，一次全量返回（表格不做分页）。

验证：`gofmt` / `go build ./...` / `go vet` / `vue-tsc --noEmit`。

### 10.20 ProductCertLimit / ProductCycleLimit / ProductRelatedLimit 插件（商品购买限制）（本轮补齐）

CBAP 包 `addon/Product{Cert,Cycle,Related}Limit.zip` 的 PHP 全部 ionCube 加密，前端 `template/admin/api/index.js`、`js/index.js`、`index.html`、`lang/zh-cn.js` 可读。四个 Product*Limit 插件中，ProductNumLimit 与站内商品自带能力等价，本轮落地其余三个（合并为一个后台页 / 三张表）。

| 维度 | 参考实现（插件前端契约） | ShitIDC 落地 |
|---|---|---|
| 实名要求 | product_id + type（1 个人/企业、2 个人认证、3 企业认证），列表 / 新增 / 编辑 / 启停 / 删除 | `product_cert_limits` 表（034 迁移），同一商品一条；下单时校验 `certifications.status='approved'`。本站实名为统一类型，三种 type 都按「已通过实名」校验（页面已注明） |
| 周期性限购 | product_id + num + cycle（天，0=永久）；周期开始时间以用户未在限制内下的第一单时间为准；修改周期影响正在限制中的周期 | `product_cycle_limits` 表；下单时按服务 created_at 逐段模拟周期窗口计数，已用数量 + 本次数量 > num 即拦截 |
| 关联限购 | product_id + related_product_id[] + type（0 捆绑 / 1 必需 / 2 互斥；插件前端仅开放「捆绑」，语言包给出三种语义） | `product_related_limits` 表（related_product_ids BIGINT[]）；捆绑 = 同批结算必须包含全部关联商品（单品下单明确提示走购物车一起结算）；必需 = 已拥有激活中的关联商品（任一）；互斥 = 不得拥有激活中的关联商品；续费时校验必需 / 互斥 |
| 数量限制 | ProductNumLimit：product_id + num，账户内该商品最大数量，已删除 / 已取消不计数 | 站内商品自带 `products.max_per_customer`（后台「商品与分组」的「单客户最多购买」，0=不限），计数口径一致（terminated / failed 不计数），视为已对齐，不重复建表 |
| 接口 | GET/POST /product_{cert,cycle,related}_limit、PUT /{id}、PUT /{id}/status、DELETE /{id} | `/admin/product-{cert,cycle,related}-limits` 同名语义（product.write + CSRF + 审计） |
| 后台页面 | 插件各自带管理页 | `/admin/product-limits` 三个页签：实名要求 / 周期性限购 / 关联限购，共用商品选择 |

口径说明（加密代码无法比对，按可见契约的最保守解释）：

- 数量与周期限制在**下单时**校验，且所有下单入口（单品下单 / 购物车 / 后付费 / 上下游兼容接口）共用同一事务内校验点；账户中的服务（含开通中、已暂停等状态）均计数，仅 terminated / failed 不计数，与插件「已删除 / 已取消不计数」一致；
- 捆绑在整车维度校验：同一结算批次即视为「同时购买」；单服务续费无法在同一单里续费关联商品，续费只校验必需 / 互斥，捆绑仅购买时校验（已知差异）；
- 插件对退款的联动（捆绑商品退款同步退款）站内未做跨订单联动：站内退款按订单退回原支付渠道（已知差异）；
- 插件 type 1/2/3 的「个人 / 企业」区分在本站实名体系中不存在，统一按已实名处理。

验证：`gofmt` / `go build ./...` / `go vet` / `vue-tsc --noEmit`。

### 10.21 IdcsmartVoucher 插件（代金券）（本轮补齐）

CBAP 包 `addon/IdcsmartVoucher.zip` 的 PHP 全部 ionCube 加密，前端 `template/admin/api/voucher.js`、`js/create_voucher.js`、`js/index.js`、`create_voucher.html`、`lang/zh-cn.js` 可读，接口与字段面取自这些文件。

| 维度 | 参考实现（插件前端契约） | ShitIDC 落地 |
|---|---|---|
| 券定义 | code（8 位且含大写 / 小写 / 数字）、price、type（private/public）、num（0=不限）、start_time/end_time（Unix 秒）、product[]、product_need[]、min_price、user_type（no_limit/no_host/need_active）、onetime、upgrade_use、renew_use、cycle[]、notes | `vouchers` 表（035 迁移）同名字段；券码 / 类型 / 面额创建后不可改（与插件表单禁用项一致） |
| 领取 / 发放 | 公开券前台领取；后台发放支持全选与按 id/username/phone/email 搜索 | `voucher_grants` 表（source=claim/grant）；`POST /vouchers/:id/claim`、`POST /admin/vouchers/:id/send`（client_id 为 "all" 或用户数组；num>0 时按总量顺序发放、发完即止并回报 skipped） |
| 核销 | 下单 / 续费 / 升降级时按订单金额抵扣（不超过应付金额），受 upgrade_use / renew_use / onetime / min_price / cycle / product / product_need / user_type 限制 | `voucherCheck` 事务内校验并返回券后金额：单品下单、购物车结算（自动挑第一条商品 / 周期匹配的明细）、续费、升级四条链路；同一用户命中最早一条未使用记录并 `FOR UPDATE` 锁定；下单即核销，订单取消不返还（与站内优惠券同口径） |
| 使用次数 / 记录 | POST /voucher/:id/times（按用户发放次数）；GET /voucher/record（client_id/page/limit/use/voucher_id） | `POST /admin/vouchers/:id/times`；`GET /admin/vouchers/record`（voucher_id / keywords / use（0 未使用 1 已使用）/ page / limit），`DELETE /admin/vouchers/record/:id` |
| 接口 | GET /voucher、POST /voucher、GET/PUT/DELETE /voucher/:id、POST /voucher/:id/{enable,disable,send,times}、GET /voucher/check、GET /voucher/record、DELETE /voucher/record/:id | `/admin/vouchers*` 同名语义（voucher.manage + CSRF + 审计）；前台 `/vouchers/my`、`/vouchers/claimable`、`/vouchers/preview`（下单前预校验抵扣金额） |
| 后台页面 | 插件自带管理页 | `/admin/vouchers`：券码 / 状态筛选，创建 / 编辑，启停，发放弹窗（全部用户 / 搜索多选 + 已发放次数），记录弹窗（来源 / 状态 / 订单 / 领取与使用时间，分页，删除） |
| 用户侧 | clientarea：领取与结算时选券 | `/vouchers`「我的代金券」：可领取公开券列表 + 我的券（状态 / 复制券码）；购物车、商品购买弹窗、服务升级弹窗支持填码预校验并随单提交 |

口径说明（加密代码无法比对，按可见契约的最保守解释）：

- 站内「优惠券」是公开的码核销式折扣，代金券是发放 / 领取式定额抵扣券，两者并存、互不替代；
- 抵扣金额 = min(券面额, 券后应付金额)，最多抵到 0、不找零；单笔订单只支持一张代金券（优惠券同样限一张）；
- 券码限定为 ASCII 大小写字母与数字共 8 位（与插件正则的字符集要求一致）；
- 服务「续费」的前台按钮是直接下单、未加券码输入框（接口已支持 `voucher_code`，其它入口均可在界面填码）；
- 退款 / 取消订单不返还代金券（插件可见契约中没有返还逻辑；与站内优惠券口径一致）。

验证：`gofmt` / `go build ./...` / `go vet` / `vue-tsc --noEmit`。

### 10.22 EventPromotion 插件（活动促销）（本轮补齐）

CBAP 包 `addon/EventPromotion.zip` 的 PHP（controller/model/validate）全部 ionCube 加密，前端 `template/admin/api/index.js`、`js/index.js`、`js/event_detail.js`、`event_detail.html`、`index.html`、`lang/zh-cn.js` 可读，接口与字段面取自这些文件（包内 README.md 只标注「插件样式Demo」，不影响前端契约的完整度）。

| 维度 | 参考实现（插件前端契约） | ShitIDC 落地 |
|---|---|---|
| 活动字段 | name、start_time/end_time（Unix 秒）、type（percent/reduce）、value（percent 为 0~100 比例 / reduce 为优惠金额）、full（满减达标金额）、products[]、clients[]（client_type all/appoint）、new_user、old_user、single_user_once、cycle_limit + cycle[]、notes | `promotions` 表（036 迁移）同名字段；金额落「分」、比例落基点（9.5% => 950）；`orders.promotion_id` 记录命中的活动 |
| 接口 | GET/POST /event_promotion、GET/PUT/DELETE /event_promotion/:id、PUT /:id/status（1 启用 / 0 停用）、GET /event_promotion/active、PUT /event_promotion/order、PUT /event_promotion/config | `/admin/promotions*` 同名语义（promotion.manage + CSRF + 审计）；列表返回 `{list, count}`、详情返回 `{event_promotion}`、active 返回 `{list, addon_event_promotion_does_not_participate}` |
| 生效方式 | 满足条件的订单自动享受折扣（无需填码）；同一订单按排序取第一个命中活动 | `promotionDiscountTx` 在下单事务内按 `sort_order, id` 逐个匹配：商品 / 周期 / 指定用户 / 新老客 / 单用户一次，命中即抵扣；单品下单与购物车结算（每条明细各自成单）都生效；优惠券、代金券在其后依次叠加 |
| 状态 | Suspended / Active / Expiration / Pending | 同四态（停用 / 待生效 / 已失效 / 启用中），由启停与时间窗实时计算 |
| 排序 | 置顶 / 置底后保存（数组顺序） | `/admin/promotions/order` 保存 `id[]` 顺序 → `sort_order`；后台「排序 / 配置」弹窗提供置顶 / 上移 / 下移 / 置底 |
| 配置 | addon_event_promotion_does_not_participate | 存入 `system_settings.event_promotion` 并原样返回；站内商品配置暂无「不参与活动」选项的消费入口（已知差异） |
| 后台页面 | 插件自带管理页（列表 / 详情页分离） | `/admin/promotions`：筛选（关键词 / 状态 / 时间点）、分页、新建 / 编辑弹窗（含快速选择时长、搜索多选指定用户）、启停、删除、排序 / 配置弹窗 |
| 前台 | clientarea（加密） | 商品页展示活动角标与购买弹窗提示；折扣金额在结算时由服务端计算（下单自动生效，无需填码） |

口径说明（加密代码无法比对，按可见契约的最保守解释）：

- 抵扣基数与优惠券一致：商品折后小计（含配置加价与初装费，不重复扣组折扣）；percent 按比例减免，reduce 需达标 `full` 才减 `value`，抵扣不超过基数；
- 单笔订单只应用一个活动（排序最靠前且命中者），活动之间不叠加——这也是插件排序 UI（置顶 / 置底）存在的意义；
- new_user = 没有已支付订单的用户；old_user = 至少有一笔已支付订单的用户（插件语言包「必须有一个已核验通过的订单」）；两个开关同时开启视为两类用户都可参与，都关闭为不限；
- single_user_once 按「该用户名下存在非取消订单且命中过该活动」判断，下单即占用（与站内优惠券 / 代金券「下单即核销」一致）；
- 续费 / 升降级订单不参与活动（活动作用于商品购买；插件契约中的参与产品 / 周期均指购买场景）；
- cycle 的 annually 归一为站内 yearly，对外仍按站内取值返回。

验证：`gofmt` / `go build ./...` / `go vet` / `vue-tsc --noEmit`。

### 10.23 CycleArtificialOrder 插件（周期人工订单）（本轮补齐）

CBAP 包 `addon/CycleArtificialOrder.zip` 的 PHP（controller/model/validate/auth/route）全部 ionCube 加密，前端 `template/admin/index.html`、`cycle_order_details.html`、`api/cycle_order.js`、`js/cycle_order*.js`、`lang/zh-cn.js` 可读，接口与字段面取自这些文件。

| 维度 | 参考实现（插件前端契约） | ShitIDC 落地 |
|---|---|---|
| 生成规则字段 | client_id、description（订单描述）、amount（订单金额）、renew_amount（续费金额）、start_time/end_time（Unix 秒，结束可空）、num + unit（day/month/year，正整数） | `cycle_artificial_orders` 表（037 迁移）同名字段；金额落「分」、时间落 TIMESTAMPTZ（对外回 Unix 秒）；`next_generate_at` / `last_generated_at` / `generated_count` 记录进度 |
| 接口 | GET/POST /cycle_artificial_order、GET/PUT/DELETE /cycle_artificial_order/:id（详情子订单支持 keywords/page/limit/orderby/sort/type/gateway/status/amount/start_time/end_time） | `/admin/cycle-artificial-orders*` 同名语义（cycle_order.manage + CSRF + 审计）；详情返回 `{list, count, cycle_order}`，支持状态 / 支付方式 / 金额 / 时间范围筛选与 id / amount / create_time 排序 |
| 生成逻辑 | 到点自动生成人工订单；首次按订单金额、之后按续费金额 | 调度器每 10 分钟扫描到期规则（Redis 锁 + 行级 FOR UPDATE，幂等），为每个到期周期生成一笔 `kind='artificial'` 订单（订单 + 明细 + 未支付账单），订单 created_at 落在周期时点上；单轮每规则最多补 30 笔，防止改短周期后刷单 |
| 变更周期 | 变更生成周期后，从最近一次已生成订单的日期开始计算（cycle_tip1） | 修改规则时按 `last_generated_at + num/unit` 重算 `next_generate_at`，超出结束时间则置空停止生成 |
| 子订单操作 | 调整价格（订单 / 子项）、标记支付（可勾选「优先扣除余额」）、删除（可勾选连带删除产品）、批量删除 | 调价：`PUT /admin/artificial-orders/:id/price`（同步订单 / 明细 / 未支付账单）；标记支付：`POST /admin/artificial-orders/:id/mark-paid`（优先扣余额，余额不足扣可用部分、余下记为线下收款，订单直接完成）；删除 / 批量删除：未支付订单作废（订单 cancelled + 账单 void），已支付订单需走退款 |
| 支付收尾 | 人工订单付款后由管理员线下处理 | `kind='artificial'` 的订单支付 / 标记支付后只置 completed + 账单 Paid，不开通服务、不入开通队列；超时自动取消（scheduler order-expire）明确跳过人工订单 |
| 时间范围展示 | start - end（空显示 ∞） | 同（列表中结束时间为空显示 ∞） |
| 后台页面 | 插件自带管理页：列表 + 详情（按用户展开子订单） | `/admin/cycle-orders`：规则列表（关键词 / 分页 / 新增 / 编辑 / 删除）+ 详情弹窗（筛选、批量删除、调价、标记支付），侧栏权限 `cycle_order.manage` |

口径说明（加密代码无法比对，按可见契约的最保守解释）：

- 「续费金额」用于第二次及以后周期生成的订单；为 0 时退回订单金额（插件只给出「订单金额 / 续费金额」两列与「首次 / 续费」语境）；
- 人工订单没有商品：`order_items.product_id` 允许 NULL，明细行承载描述与金额；因为不存在服务，插件的「删除订单同时删除产品」选项在站内无对应动作（删除即作废未支付订单）；
- 037 迁移同时把 `orders.kind` CHECK 放开到 `('new','renewal','upgrade','artificial')`——此前 `store_upgrades.go` 写入的升级订单会被旧约束拒绝，属既有隐性缺陷，本轮一并修复；
- 生成规则删除后子订单保留（关联置空），订单 / 账单 / 支付记录不物理删除；
- 站内人工订单固定以 CNY 记账（与站点主币种一致），金额前缀沿用 ¥。

验证：`gofmt` / `go build ./...` / `go vet` / `vue-tsc --noEmit`。

### 10.24 IdcsmartInvoice 插件（发票管理）（本轮补齐）

CBAP 包 `addon/IdcsmartInvoice.zip` 的 PHP（controller/model/validate/auth/route）全部 ionCube 加密，前端 `template/{admin,clientarea}`（api / js / lang / 页面）可读，接口、字段与状态机取自这些文件。状态机：pending 待审核 / unpaid 待支付 / wait_send 待发出 / sent 已发出 / reject 已驳回 / cancel 作废 / flushed 已冲红。

| 维度 | 参考实现（插件前端契约） | ShitIDC 落地 |
|---|---|---|
| 发票抬头 | invoice_title：title_type 公司 company / 个人 person、title、invoice_type 普票 normal / 专票 special、公司地址、税务登记号、开户行、开户账号 | `invoice_titles` 表（038 迁移）同名字段；专票必填税务登记号；用户维度 CRUD（/invoice_title），后台只读 + 批量删除 |
| 收件地址 | invoice_address：rec_type 纸质 paper / 电子 email、收件人、省市区、详细地址、电话、邮箱、收件网址、默认地址 | `invoice_addresses` 表；纸质必填地址与电话、电子必填邮箱或网址；默认地址同用户唯一（保存时互斥） |
| 发票项目 | invoice_project：名称、普票税率 / 收税金额、专票开关、专票税率 / 收税金额 | `invoice_projects` 表；税率与收税比例对外按百分比（内部基点，100 = 1%） |
| 发票设置 | invoice_config：invoice_manage 开关、pre_invoice 允许未支付订单、across_year_invoice 允许往年发票、快递方式 parcel[{id,name,price}] | 设置键 `idcsmart_invoice`；用户端在 invoice_manage 关闭时统一返回 403 INVOICE_DISABLED；快递方式保存时清洗（空名称忽略、价格非负、编号补全） |
| 申请开票 | 勾选订单（可多选）+ 抬头 + 收件 + 项目 + 格式 pdf / ofd / xml，先试算税金与快递费 | `POST /invoice/price` 试算（price / tax_rate / tax_fee / tax_price / parcel_name / parcel_price / total / fee / host[]，字段名与插件一致）；`POST /invoice` 创建，票面合计 = 订单合计 + 税金 + 快递费 |
| 可开票订单 | 已支付订单；pre_invoice 开启后可选未支付订单；跨年订单需 across_year_invoice；已有有效申请的订单不可重复选 | `GET /invoice_request?status=Paid|Unpaid`；有效申请状态为 pending / unpaid / wait_send / sent（cancel / reject / flushed 放开重开），跨年按订单创建年份判断 |
| 费用单 | 税金 / 快递费需支付后才能开票 | 费用大于 0 时创建 `kind='artificial'`、`kind_detail='invoice_fee'` 的人工订单并置申请 unpaid（明细为费用摘要、7 天到期、币种随所选订单）；钱包 / 在线支付完成后由结算流程把申请推进到 pending，支付不开通服务；人工订单不参与超时自动取消 |
| 用户操作 | 列表 / 详情 / 作废 / 下载发票文件 | `GET /invoice`、`GET /invoice/:id`、`DELETE /invoice/:id`（pending / unpaid / reject 可作废，未支付费用单一并作废）、`GET /invoice/:id/invoice_filename` 下载 |
| 后台操作 | 审核通过 / 驳回 / 发出（纸质快递单号 + 快递单照片、电子上传发票文件）/ 冲红 / 删除文件 | `/admin/invoice*`：confirm（pending → wait_send）、reject（pending / wait_send → reject，原因必填）、send（wait_send → sent；纸质须快递单号、电子须先上传文件；支持 multipart 快递单照片）、flush（sent → flushed 冲红）、upload（pdf / ofd / xml / zip，ofd 校验 PK 头）、文件下载 / 删除与快递单照片查看，全部写审计 |
| 上传存储 | 插件自带上传目录 | 本机 `uploads/invoices/<申请号>/`（uuid 命名、强制下载、路径穿越校验）；配置 OSS 后转存 private 桶并 302 签名链接（与巡查记录共用上传基础设施） |
| 迁移 | — | `038_idcsmart_invoice.sql`（5 张表 + invoice.manage 权限，后台角色自动授权）、`039_invoice_parcel_image.sql`（快递单照片列） |
| 前台页面 | 插件自带用户端 / 后台页面 | 用户端 `/invoice`（申请开票 / 开票记录 / 发票抬头 / 收件地址，顶部导航「发票」）；后台 `/admin/invoices`（发票申请 / 设置 / 项目 / 用户抬头 / 收件地址，侧栏「发票管理」） |

口径说明（加密代码无法比对，按可见契约的最保守解释）：

- 站内既有「账单」（/invoices，billing 口径）与插件「发票」是两套概念：本轮新增发票页面，不改动账单口径；
- 申请创建时快照抬头与收件信息（含税号 / 银行 / 地址），此后修改或删除抬头 / 地址不影响历史申请；
- OFD / XML 仅支持电子发票（收件方式为电子），纸质发票固定 pdf；
- 作废 / 驳回 / 冲红都不物理删除申请：作废仅放开订单重开并作废未支付费用单，已支付费用单需走退款流程；
- 后台「查看快递单照片」对 OSS 存储返回签名图片地址，本机存储则按附件下载处理。

验证：`gofmt` / `go build ./...` / `go vet` / `vue-tsc --noEmit`。

### 10.25 EmailNoticeAdmin 插件（管理员邮件通知）（本轮补齐）

CBAP 包 `addon/EmailNoticeAdmin.zip` 主类加密，前端资产可读：表格列为 `name_lang`（动作名称）/ `email_name`（邮件接口）/ `email_template`（邮件模板）/ `notify_personnel`（通知人员，管理员多选）/ `email_enable`（启用开关）；接口 `GET /email_notice_admin` 列表、`PUT /email_notice_admin` 保存，前端校验「选了邮件接口必须选邮件模板」。本轮按该契约落地「邮件通知」：

| 维度 | 参考实现 | ShitIDC 落地 |
|---|---|---|
| 行来源 | 插件动作清单 | 事件总线 20 个核心事件（internal/events 清单，附中文动作名） |
| 邮件接口 | 插件邮件通道列表 | mail_providers 通道下拉；留空 = 跟随系统默认通道 / 内置 SMTP，选中后按该通道发送（队列载荷带 provider） |
| 邮件模板 | 插件邮件模板列表 | mail_templates 下拉；正文支持 {{占位符}}（event / event_name / time 与事件字段，另有 uid、user_email 等别名） |
| 通知人员 | 插件管理员树 | 持有非 customer 角色的员工账号（多选，带角色名展示） |
| 校验 | 选了 email_name 必须选 email_template | 服务端同规则：启用必须选模板与通知人员；选了接口必须选模板 |
| 后台接口 | GET /email_notice_admin、PUT /email_notice_admin | /admin/email-notice-admin（GET / PUT，`email_notice.manage` 权限 + CSRF + 审计） |
| 发送时机 | 插件钩子 | internal/notify 订阅事件总线，server / worker 两端都接线；异步发送，失败只记日志不影响业务 |
| 后台页面 | 插件自带管理页 | /admin/email-notice「邮件通知」页（动作 / 接口 / 模板 / 人员 / 启用 + 保存） |
| 迁移 | — | 040_email_notice_admin.sql（email_notice.manage 权限，后台角色自动授权） |

说明：注册验证、工单回复等站内自有邮件是各自流程直发，不受此配置影响；本插件只增加「事件 → 给管理员发通知邮件」这条可配置通道。

验证：`gofmt` / `go build ./...` / `go vet ./...` 与 `npx vue-tsc --noEmit` 全部通过。


### 10.26 HostTransfer 插件（产品转移）（本轮补齐）

CBAP 包 `addon/HostTransfer.zip` 主类加密，前端资产可读：后台页展示产品转移记录（产品ID / 商品名称 / 商品标识 / 原始用户 / 目标用户 / 迁移时间 / 操作人 / 备注），接口 `GET /host_transfer_log`；转移动作在「用户详情 - 产品信息页」执行，语言包明确「关联产品自动一起迁移」「不迁移订单信息」「VPC/安全组/付费镜像同步到目标用户」。本轮按该契约落地「产品转移」：

| 维度 | 参考实现 | ShitIDC 落地 |
|---|---|---|
| 转移对象 | 产品（服务） | 服务（services），待开通 / 生效 / 暂停 / 开通失败等未删除状态都可以转移 |
| 关联产品 | 同单关联商品自动一起迁移 | 同一订单下仍属于原用户的未删除服务整单迁移（改 user_id） |
| 订单信息 | 不迁移 | 订单、账单、支付记录保持原用户归属不动 |
| VPC/安全组/镜像 | 同步到目标用户 | 本站无这三类资源，无需镜像同步 |
| 记录字段 | 产品ID/商品名称/商品标识/双方用户/时间/操作人/备注 | service_transfers 表（041 迁移）：service_id、from/to 用户、operator、remark、created_at |
| 后台接口 | GET /host_transfer_log | GET/POST /admin/service-transfers（service.manage 权限 + CSRF + 审计；POST 校验目标用户为正常状态，拒绝转给当前所有者与已删除产品） |
| 发起位置 | 用户详情-产品信息页 | 「服务管理」行操作「转移」与「用户详情 → 机器」tab 的「转移」按钮，共用 ServiceTransferModal（选目标用户 + 备注） |
| 后台页面 | 插件自带记录页 | /admin/service-transfers「产品转移」页：关键词查询 + 刷新；页面提示发起位置 |

说明：商品标识取 products.provider_product_ref（本站没有独立的商品 code 列）；目标用户按 UID / UUID / 邮箱解析；同一产品多次转移会分别留记录。

验证：`gofmt` / `go build ./...` / `go vet ./...` 与 `npx vue-tsc --noEmit` 全部通过。

### 10.27 FlowPacket 插件（流量包）（本轮补齐）

CBAP 包 `addon/FlowPacket.zip` 主类加密，前端资产可读：后台两个页签「流量包订单」（列：ID / 用户 / 流量包 / 关联产品 / 下单时间 / 下单金额 / 支付状态，状态为 未付款 / 已付款 / 已取消 / 已退款，行操作查看订单 / 删除，关键词匹配流量包名 / 产品名 / 用户）与「流量包管理」（列：ID / 名称 / 流量GB / 售价 / 关联商品 / 开关 / 库存 / 备注；新增表单字段：名称、流量（G）、售价、可用库存 + 库存开关、关联商品（多选，必填）、备注）；接口 `GET|POST /flow_packet`、`PUT|DELETE /flow_packet/{id}`、`PUT /flow_packet/{id}/status`、`GET /flow_packet_order`、`DELETE /flow_packet_order/{id}`、`GET /module/product`（module=mf_cloud/mf_dcim）。本轮按该契约落地「流量包」：

| 维度 | 参考实现 | ShitIDC 落地 |
|---|---|---|
| 流量包字段 | 名称 / 流量GB / 售价 / 可用库存+开关 / 关联商品 / 备注 / 开关 | flow_packets 表（042 迁移）：name、capacity_gb、price_cents、stock、stock_enable、notes、active |
| 关联商品 | 多选商品（mf_cloud / mf_dcim） | flow_packet_products 多对多表（存商品公开 ID）；新增 / 编辑至少选择一个商品 |
| 后台接口 | /flow_packet 五件套 + /flow_packet_order | GET/POST /admin/flow-packets、PUT/DELETE /admin/flow-packets/:id、PUT /admin/flow-packets/:id/status、GET /admin/flow-packet-orders、DELETE /admin/flow-packet-orders/:id（flow_packet.manage 权限 + CSRF + 审计；列表支持关键词与状态过滤） |
| 后台页面 | 两个页签 | /admin/flow-packets「流量包」页：流量包订单（关键词 / 支付状态查询、删除）与流量包管理（新增 / 编辑 / 启停 / 删除，编辑时关联商品整体替换） |
| 订单字段 | 用户 / 流量包 / 关联产品 / 下单金额 / 支付状态 / 下单时间 | flow_packet_orders 表：user_id、packet_id + packet_name/capacity_gb 快照、service_id + service_name 快照、amount_cents、status、created_at/paid_at |
| 用户端 | clientarea/IndexController（PHP 加密不可读） | 「流量包」页：列出上架流量包与每个包适用的名下产品（生效中 / 暂停中 / 已暂停），下单后用余额支付；余额不足保留未付款订单，可在页内支付或取消 |
| 流量到账 | 插件把流量加到产品上 | 本站服务是接口无关资源，付款后广播 service.updated（kind=flow_packet_paid）并留订单记录，实际到账依赖上游能力 / 人工处理 |

说明：插件订单状态为 Unpaid/Paid/Cancelled/Refunded，本站按站内小写口径落为 unpaid/paid/cancelled/refunded，后台筛选与标签四种都支持；「已退款」暂只保留口径（插件后台同样只有查看与删除两个动作）。用户端流程按本站风格实现（插件 clientarea 的 PHP 与模板不可读），用户端路由挂在 service.read 权限下。

验证：`gofmt` / `go build ./...` / `go vet ./...` 与 `npx vue-tsc --noEmit` 全部通过。

### 10.28 ClientCare 插件（客户关怀）（本轮补齐）

CBAP 包 `addon/ClientCare.zip` 主类加密，前端资产可读：后台「客户关怀」列表（列：通知标题 / 推送内容 / 推送时间 / 推送周期 / 推送状态，状态含 Wait 待执行 / Exec 执行中 / Suspended 已暂停 / Expired 已失效 / Finish 已完成，行操作启停 / 删除）与新建表单（通知标题、通知形式 1 站内信 / 2 短信+邮件、推送内容、邮件标题、邮件通道与模板、短信通道与模板、推送时间范围、推送周期 onetime/day/week/month + 周几 / 月几 / 时分、同用户重复发送开关、推送目标条件构造器）；用户端 `GET /client_care/mail/{id}` 站内信详情（含上一篇 / 下一篇）。本轮按该契约落地：

| 维度 | 参考实现 | ShitIDC 落地 |
|---|---|---|
| 任务字段 | title / type / content / subject / email_name / sms_name / sms_template_id / push_start_time / push_end_time / send_cycle / week_day / month_day / hour / minute / repeat_send / push_object | client_care_jobs 表（043 迁移）按同名落库；type 1 站内信 / 2 短信+邮件 |
| 推送周期 | onetime / day / week / month + 周几 / 月几 / 时分 | 调度器每 1 分钟执行到期任务并推进 next_run_at；未到开始时间自动顺延，超出结束时间置为已失效 |
| 圈人条件 | push_object：condition1 client/host/server；二级 client/register_time/last_login_time/host_num/active_host_num/owner_special_product、host/status/purchase_time/termination_time、server/product；三级比较符 >= 或 <、枚举数组；condition4 数值或日期；condition5 day/date | 条件翻译为 users WHERE（含服务 EXISTS 子查询）：状态映射 未付款→pending、待开通→provisioning、生效中→active、已暂停→suspended、已删除→terminated、开通失败→failed、已取消忽略；指定产品 / 接口按公开 ID 子查询匹配 |
| 后台接口 | /client_care 列表 / 新建 / 启停 / 删除 / 名单预览 / 发送预览 | GET/POST /admin/client-care、PUT /admin/client-care/:id/status、DELETE /admin/client-care/:id、POST /admin/client-care/recipients（返回 count + 名单）、GET /admin/client-care/options（产品 / 接口 / 邮件通道 / 邮件模板）、GET /admin/client-care/users（client_care.manage 权限 + CSRF + 审计） |
| 投递 | 站内信 + 邮件（+短信） | 到点写入 client_care_mails 收件箱并生成站内通知（链接 /messages/{id}）；邮件类任务按 email_name（邮件通道公开 ID，空 = 默认通道）经 mail.send 队列投递；短信自定义内容本站通道只支持验证码模板，暂不投递（字段保留） |
| 用户端 | 站内信详情（上一篇 / 下一篇） | 用户中心「消息」页：列表 + 详情（自动标记已读、上一篇 / 下一篇）；接口 GET /client-care/mails、GET /client-care/mails/:id、POST /client-care/mails/:id/read |

说明：repeat_send 关闭时同一任务对同一用户只投递一次（按 job_id + user_id 去重）；推送时间点（时 / 分）按服务器本地时区解释；邮件模板仅用于快速填充邮件标题，邮件正文取推送内容。

验证：`gofmt` / `go build ./...` / `go vet ./...` 与 `npx vue-tsc --noEmit` 全部通过。

### 10.29 TicketInternalPremium 插件（内部工单）（本轮补齐）

CBAP 包 `addon/TicketInternalPremium.zip` 主类加密，前端资产可读：后台四个页面——「内部工单」列表（关键词 / 类型 / 状态 / 发起人 / 跟进人 / 领取人筛选，超时角标；行内接单 / 转单 / 关闭 / 评分；新建弹窗含关联用户与产品、关联工单、紧急程度、备注）、工单配置（部门设置 / 工单状态 / 预设回复 / 其他设置）、定时工单（循环周期 + 日期范围 + 触发时间）、工单统计；用户端无独立页面（面向内部协作，与用户工单面板配套）。本轮按该契约落地：

| 维度 | 参考实现 | ShitIDC 落地 |
|---|---|---|
| 部门与类型 | 部门（名称 / 管理人员 / 主管）+ 部门下类型（名称 / 处理时限小时） | ticket_internal_departments / ticket_internal_types（044 迁移）；编辑部门即重建其类型；有工单引用时拒绝删除 |
| 工单状态 | 默认 4 个系统状态（待接单 / 待回复 / 已回复 / 已关闭），可自定义名称 / 颜色 / 完结状态；系统状态不可改删 | ticket_internal_statuses（key 唯一 + system 保护）；后台「工单配置 → 工单状态」行内编辑 / 新增 / 删除 |
| 工单流转 | 新建 / 接单 / 回复 / 转单 / 处理完成 / 关闭 / 评分 | 编号 `TI+日期+5 位序号`；order_button 开启时回复前需先接单，follow_limit 开启时仅跟进人可回复；转单校验目标人员权限并写入转交备注；处理完成可选同时关闭 |
| 超时判定 | 处理时限（小时）内完成；剩余不足 15% 视为即将超时 | 截止 = 创建时间 + 类型处理时限；完结且按期完成记「按期」，否则显示超时 / 即将超时角标；发站内提醒受 will_timeout_notice 开关控制 |
| 详情与协作 | 沟通记录（回复 + 备注）、预设回复、操作日志 | ticket_internal_replies / notes / logs；详情页回复 + 预设回复弹窗 + 添加备注 + 日志弹窗；回复时自动补记第一处理人 |
| 评分 | 发起人评分 / 主管评分，形成综合分 | 0.5–5 星；被评分主体为第一处理人；两类评分分别落库，综合分与排名按评分角色（creator / director）过滤 |
| 定时工单 | 循环周期（每 N 天 / 自然月 / 年）+ 日期范围 + 触发 HH:mm | ticket_internal_cron_jobs；调度器 @every 1m 生成到期工单并推进 next_run_at，超出结束时间自动置停 |
| 统计 | 单量 / 处理时长 / 评分 / 超时占比；按部门与个人排名；评分角色筛选 | 后台「内部工单统计」页：日期范围 + 评分角色（全部 / 发起人 / 主管）+ 范围（所有部门 / 按部门 / 按个人），统计卡 + 平均分排名 + 时长排名进度条 |
| 其他设置 | 接单按钮 / 跟进人限制 / 时限提醒 / 刷新时间 | ticket_internal_config（order_button / follow_limit / will_timeout_notice / refresh_time）；调度器 @every 5m 扫描剩余不足 15% 的工单发站内通知 |
| 后台接口 | /admin/ticket-internal 全套 | tickets（列表 / 新建 / 详情 / 保存 / 日志 / 回复 / 接单 / 转单 / 关闭 / 处理完成 / 评分 / 备注）、department / status / prereply / config / staff / hosts / cron / statistics / rank（评分与时长按部门 / 个人）；权限 `ticket_internal.manage`（admin 默认授权） |

说明：列表 / 详情中的附件字段保留但未接文件上传；提醒与通知均为站内通知；用户端无独立入口（内部工单面向管理员协作）。

验证：`gofmt` / `go build ./...` / `go vet ./...` 与 `npx vue-tsc --noEmit` 全部通过。

### 10.30 TicketPremium 插件（用户工单高级版）（本轮补齐）

CBAP 包 `addon/TicketPremium.zip` 主类加密，前端资产可读：后台四个页面——工单列表（关键词 / 类型树 / 状态 / 用户 / 最后回复人 / 领取人 / 时间范围筛选与列表刷新，超时角标；行内接单 / 处理完成 / 关闭）、工单配置（部门设置 / 工单状态 / 预设回复 / 其他设置）、工单统计（单量 / 评分 / 超时占比与部门、个人排名）、代客建单；用户端工单页（提交工单 + 部门类型 + 关联产品 + 工单须知）、工单详情（沟通记录 / 附件 / 催单 / 评分）。本轮按该契约落地：

| 维度 | 参考实现 | ShitIDC 落地 |
|---|---|---|
| 部门与类型 | 部门（名称 / 管理人员 / 主管）+ 类型（处理时限小时） | ticket_departments / ticket_types（045 迁移）；有工单引用时拒绝删除部门 / 类型 |
| 工单状态 | 默认状态（待处理 / 待回复 / 已关闭）+ 自定义名称 / 颜色 / 完结状态 | ticket_statuses（key 唯一 + system 保护）；状态带颜色展示，完结状态自动同步 finished / finish_time 并计入「已处理」 |
| 工单流转 | 用户提交 → 客服接单 → 回复 → 处理完成（可同时关闭）→ 用户评分 | 编号 `T+日期+5 位序号`；ticket_receive_reply=1 时未接单不可回复，ticket_follow_reply=1 时仅领取人可回复；处理完成写 finish_time |
| 详情与协作 | 沟通记录 + 内部备注 + 操作日志 + 预设回复 | ticket_notes / ticket_logs / ticket_prereplies；详情页回复（可带附件）、备注、预设回复弹窗、日志弹窗、编辑 / 删除回复与备注 |
| 关联产品与附件 | 工单选关联产品，回复可带附件 | tickets.host_ids 关联服务；回复附件走 ticket_attachments（单文件 ≤5MB、扩展名与内容双重校验） |
| 催单 | 用户催单，15 分钟内不可重复 | tickets.last_urge_time / urge_count；催单写站内通知给客服并发送邮件 |
| 评分 | 满意度 / 服务态度 / 处理时效三项 0.5–5 星，完成后评分一次 | tickets.is_score / satisfaction / attitude / processing_time / score_time；仅已处理完成的工单可评分且只可评一次 |
| 转内部工单 | 用户工单转内部工单继续流转 | /admin/ticket-premium/tickets/:id/turn-internal 复用内部工单表并写来源日志 |
| 统计 | 单量 / 处理时长 / 评分 / 超时占比；按部门与个人排名 | 后台「工单统计」页：日期范围 + 范围（所有部门 / 按部门 / 按个人），统计卡 + 平均分排名 + 时长排名 |
| 邮件模板 | 客户建单 / 客户回复 / 客服回复 / 客户关闭 四个内置模板 | ticket_client_create / ticket_client_reply / ticket_admin_reply / ticket_client_close，可在「邮件模板」页覆盖 |
| 其他设置 | 接单后回复 / 仅领取人回复 / 工单须知 / 刷新时间 | ticket_config（ticket_receive_reply / ticket_follow_reply / ticket_notice_open / ticket_notice_description / refresh_time）；用户提交页展示工单须知 |
| 后台接口 | /admin/ticket-premium 全套 | tickets（列表 / 代建 / 详情 / 回复 / 接单 / 保存 / 状态 / 处理完成 / 备注 / 日志 / 转内部）、department / status / prereply / config / staff / hosts / statistics / rank（评分与时长按部门 / 个人）；用户侧 /tickets/meta、departments、hosts、:id/urge、:id/score；权限 `ticket.manage` |

说明：短信通知未接入（保留与插件一致的邮件 + 站内通知）；「字段说明」未单独建模——部门 / 类型即其等价结构；用户新建工单的附件在回复环节上传；列表与详情中的操作日志、催单、评分均落库可查。

验证：`gofmt` / `go build ./...` / `go vet ./...` 与 `npx vue-tsc --noEmit` 全部通过。

### 10.31 IdcsmartRecommend 插件（推介计划）（本轮补齐）

CBAP 包 `addon/IdcsmartRecommend.zip` 的 PHP（controller / model / logic / validate / route / lang）全部 ionCube 加密，前端资产可读：后台两页——「奖励记录」（推介人 / 被推介用户 / 商品 / 状态筛选，行内确认 / 冻结 / 解冻 / 置无效 / 改奖励金额 / 删除，预设无效回复弹窗）与「推介配置」（奖励存款 / 确认天数 / 最低提现金额 / 提现手续费 / 默认推介链接 / 系统页面链接 / 商品新购与续费比例 / 主打推介产品）；用户端「推介计划」页（未开启引导开启、可提现 / 已提现 / 待确认 / 已确认四卡、推介链接与建议推介产品、自定义链接、推介记录、提现记录、完整推介政策弹窗）；邮件模板 `recommend_notice.html`。接口契约取自 `template/admin/api/recommend.js` 与 `template/clientarea/api/referral.js`，字段与文案取自两端的 JS 语言包。本轮按该契约落地：

| 维度 | 参考实现（插件前端契约） | ShitIDC 落地 |
|---|---|---|
| 奖励记录 | promoter / username / product_name / type（new 新购、renew 续费）/ buy_amount / ratio / awards_amount / surplus_time / status（Pending 待确认、Active 已确认、Frozen 冻结、Invalid 无效） | recommend_awards（046 迁移）：promoter_id / user_id / product_id / product_name / type / buy_amount_cents / ratio / awards_amount_cents / status / invalid_reason；`surplus_time` 为待确认剩余秒数，列表按确认时间换算展示 |
| 推介配置 | awards 奖励存款 / day 确认天数（0=即刻确认，默认 14 天）/ withdraw_min / withdraw_handling_fee / default_url / system_url 多行 / ratios（product_id、ratio、amount、renew_ratio、renew_amount）/ products 主打产品 | system_settings.recommend 标量配置 + recommend_ratios / recommend_products 两表；`/admin/recommend/config` 读写，金额落「分」、比例落百分数 |
| 奖励产生 | 被推介用户购买商品支付成功后按商品比例产生奖励；触发最低金额按「同一订单相同商品合计金额」计算 | 支付完成收尾（afterPaymentCompleted）调用 `AccrueRecommendAwards`：按订单商品分组求和 → 校验比例与最低金额 → 幂等键 `recommend:{订单}:{商品}:{类型}` 写记录；续费订单（orders.kind=renewal）取续费比例，其余取新购比例 |
| 确认与提现 | 奖励确认后才可提现（剩余确认时间倒数） | 确认天数内为 Pending，到期（或天数 0 即创建时）自动转 Active；可提现 = 已确认 − 已申请（待审核 / 待打款 / 已打款占用，驳回释放）；最低提现金额与提现手续费按配置校验 |
| 后台接口 | GET/POST `/recommend/config`、GET `/recommend`、PUT `/{id}/awards_amount`、PUT `/{id}/invalid\|active\|frozen\|unfrozen`、DELETE `/{id}`、`/recommend/prereply` 五件套 | `/admin/recommend`（列表）、`/admin/recommend/config`（读写）、`/admin/recommend/awards/:id/...`（awards_amount / active / frozen / unfrozen / invalid + DELETE）、`/admin/recommend/prereplies` 五件套、`/admin/recommend/withdrawals`（提现审核）；权限 `wallet.adjust` + CSRF + 审计 |
| 用户端接口 | `/recommend/promoter`（GET/POST）、`/recommend/description`、`/promoter/system_url`、`/promoter/url`（GET/POST/DELETE）、`/recommend/copy_link`、`/recommend/products`、`/recommend`、`/withdraw`、`/recommend/config`、POST `/recommend/withdraw` | `/recommend` 与 `/recommend/promoter`、`/description`、`/promoter/system_url`、`/promoter/url`、`/copy_link`、`/products`、`/config`、`/withdrawals`、`/withdraw` 同构落地；提现记录由核心 `/withdraw` 收归本模块（本站无独立提现模块），管理员在「推介计划 → 提现审核」打款 |
| 预设无效回复 | 列表 + 新增 / 编辑 / 删除；内置项（status=Active）不可编辑删除 | recommend_prereplies（system 标记内置项，seed 一条默认回复）；置无效时可选预设或自定义原因（≤500 字） |
| 邮件通知 | recommend_notice.html 模板 | 奖励入账给推介人写站内通知 + 邮件（模板名 `recommend_notice`，可在「邮件模板」页覆盖；未配置时用内置兜底文案） |

口径说明（加密代码不可读，按可见契约的最保守解释实现）：

- 与旧「推广返佣」的关系：站内原有 `/referral`（按订单总额固定比例即时返到余额）保留；当被推介用户的推荐人开启过推介计划时，该订单改走本模块奖励记录、不再即时返佣，反之维持旧逻辑，两者互斥以避免同一订单重复发放；
- 初始奖励存款（awards）在首次开启推介计划时发放一条 `type=init` 的记录（同样走确认天数），列表展示为「奖励存款」；
- 插件按主机（host_id）筛选商品，本站订单未关联服务实例，等义落为按商品（product_id）筛选与展示；
- 推荐关系沿用站内邀请码（users.referred_by，注册时填写或链接 `?ref=` 自动带上）；插件自定义链接的自定义后缀以 `from=` 标记来源，不参与奖励计算；
- 提现打款为线下流程（后台标记「已打款」），不从钱包余额出账；驳回时释放占用的可提现金额；
- 插件无同名旧功能，本模块接口前缀 `/recommend` 与站内既有 `/referral` 并存不冲突。

验证：`gofmt` / `go build ./...` / `go vet ./...` 与 `npx vue-tsc --noEmit` 全部通过。
### 10.32 ProductDropDownSelect 插件（商品下拉优化）（本轮补齐）

CBAP 包 `addon/ProductDropDownSelect.zip` 主类与路由加密，前端 `template/admin`（api / index.html / js / lang）完整可读：只有一个配置面——后台选择「产品信息详情页的商品下拉框」的下拉样式（default 平铺 / first_group 一级分组 / second_group 二级分组 / first_second_group 一级 + 二级分组），接口 `GET/PUT /product_drop_down_select/config`。本轮按该契约落地：

| 维度 | 参考实现（前端契约） | ShitIDC 落地 |
|---|---|---|
| 配置 | GET/PUT config，样式四选一 | `system_settings.product_drop_down_select`（style 一键），`GET/PUT /admin/product-dropdown-select`（product.write + CSRF + 审计），样式白名单校验 |
| 后台页面 | 四张样式卡 + 示例下拉 | `/admin/product-dropdown` 四张样式卡，分组预览按在售商品实时聚合（`GET` 一并返回分组视图） |
| 用户端 | host 详情页商品下拉按样式渲染 | 「服务 → 升降级」弹窗的目标商品下拉：非 default 样式按商品分组聚合（naive-ui 分组选项），样式经 `GET /product-dropdown-select` 下发；`upgrade-plans` 返回补充 `group_name` |

口径说明：站内商品分组只有一级（product_groups），second_group / first_second_group 的呈现与 first_group 相同——分组为一级、商品为叶子；样式值按插件原样保存，将来出现多级分组无需迁移。插件配置面里的「请选择商品」示例选择器属于页面内演示数据，无服务端语义，不落地。

验证：`gofmt` / `go build ./...` / `go vet ./...` 与 `npx vue-tsc --noEmit` 全部通过。

### 10.33 WanyunResource 插件（万云资源管理）（本轮补齐）

CBAP 包 `addon/WanyunResource.zip` 的 PHP（controller / model / route / sidebar）全部 ionCube 加密，前端 `template/admin` 五个页面（ip_manage / node_manage / vlan_manage / fiber_manage / fiber_core_manage）与 `api/wanyun_resource.js`、三语语言包可读，接口与字段面完整。本轮按该契约落地为手工资源台账（047 迁移，9 张表）：

| 维度 | 参考实现（前端契约） | ShitIDC 落地 |
|---|---|---|
| IP 段 | `GET /wanyun_resource/ips`：段 + 子网（ips_sub）+ IP 数 / 可用 / 已用 / 分组 / 备注；`GET /wanyun_resource/ip` 地址明细（ip / 分配时间 / 备注） | `wy_ip_segments`（parent_id 两级）+ `wy_ip_addresses`（ip / 分配人 / 使用人 / 使用单位 / 已用 + used_at / 备注），可用已用实时统计；地址明细支持手工登记 / 编辑 / 删除（插件由 DCIM 同步，本实现手工维护） |
| 节点 | `GET/POST/PUT/DELETE /wanyun_resource/node`：名称（≤20）+ 类型 + 自定义字段 | `wy_nodes` + 类型 `wy_node_types`（引用中拒绝删除）+ 自定义字段值校验（必填 / 下拉取值） |
| 自定义字段 | 节点与纤芯各一套：field_name（≤10）/ text·dropdown / field_option（英文逗号分隔）/ is_required / show_list / 拖动排序 | `wy_custom_fields`（scope=node/fiber_core）+ `wy_custom_field_values`；`/show` 开关与 `/drag`（移到 prev 之后，整表重排权重）语义与站内其它拖动一致 |
| VLAN | `GET/POST/PUT/DELETE /wanyun_resource/vlan`：vlanid / 名称 / 类型 / 分配人 / 使用人 / 使用单位 / 途径节点[] / 状态 / 备注；`PUT /:id/status` | `wy_vlans` + `wy_vlan_types` + `wy_vlan_nodes`；关键词（名称 / 编号 / 分配人 / 使用人 / 使用单位）与状态过滤 |
| 光纤 / 纤芯 | 光纤：fiber_num / 所属 / 芯数 / 开通单位 / 施工单位 / 联系人 / 项目 / 价格 / 途径节点[]；纤芯：编号 / 途径节点[] / 备注 / 自定义字段 | `wy_fibers` + `wy_fiber_nodes` + `wy_fiber_cores`（按芯数生成、只增不减）+ `wy_fiber_core_nodes` + 补芯接口；纤芯自定义字段复用 scope=fiber_core |
| DCIM 接口 | `GET/PUT /wanyun_resource/dcim_config`（选一个 server）+ `/sync` | 明确不落地：DCIM 客户端协议（`idcsmart_dcim/Dcim.php`）ionCube 加密不可读，数据全部手工维护，后台页不提供假入口 |

权限统一 `wanyun_resource.manage`（写操作 CSRF + 审计，动作前缀 `wanyun.*`）。

验证：`gofmt` / `go build ./...` / `go vet ./...` 与 `npx vue-tsc --noEmit` 全部通过。

### 10.34 ManualResource 插件（手动资源）（本轮补齐）

CBAP 包 `addon/ManualResource.zip` 的 PHP（controller / logic / model / validate / route）全部 ionCube 加密，前端 `template/admin`（api / index.html / js / 三语语言包）与随包 noVNC 完整可读。本轮按该契约落地（048 迁移）：

| 维度 | 参考实现（前端契约） | ShitIDC 落地 |
|---|---|---|
| 供应商 | `GET/POST/PUT/DELETE /manual_resource/supplier`：名称 / 联系方式 / 备注 | `manual_suppliers` 同名语义；删除后名下资源的供应商置空 |
| 资源台账 | `GET/POST/PUT/DELETE /manual_resource`：主 IP（必填）/ 附加 IP（换行多个）/ 供应商 / 成本 / 配置 / 备注 / 系统用户名密码 / 控制方式 ipmi·客户端 / IPMI IP、端口、版本 / DCIM 客户端地址、服务器 ID / 控制用户名密码 / 到期时间 | `manual_resources` 全字段；关键词匹配 IP / 配置 / 备注，供应商与状态过滤 |
| 分配 | 确认分配该资源 / 确认空闲该资源（关联 client + host，到期时间） | `POST /manual-resources/:id/assign`（按服务选择、客户随服务行带出，服务未终止才可分配）与 `/idle`（解除关联置 idle） |
| 电源操作 | `GET /:id/status`、`POST /:id/{on,off,reboot}`：状态列显示 开机 / 关机 / 错误 | IPMI 模式走 `internal/ipmi`（见下），操作后落最近电源状态 power_status；客户端（DCIM）模式明确返回「不支持」（与插件语言包 manual_text28「不支持」一致） |
| 任务进度 | `GET /:id/task_status`、`POST /:id/cancel_task`、`GET /:id/os`、重装 / 救援 / 破解密码 | 不落地：重装 / 救援 / 破解密码与 OS 列表由加密的 `IpmiLogic` / `DcimClientLogic` 驱动，协议不可读；IPMI 电源命令是同步短操作，无任务队列语义 |
| 控制台 | 随包 noVNC（view/noVNC） | 不落地：控制台经由 DCIM 客户端（加密）；IPMI HTML5 KVM 是厂商私有实现 |

**internal/ipmi（纯标准库 IPMI 2.0 / RMCP+ 客户端）**：协商 RAKP-HMAC-SHA1 + HMAC-SHA1-96 + AES-CBC-128（cipher suite 3 语义）。密钥推导与帧格式按 ipmitool lanplus 实现逐项对齐——SIK = HMAC(Kuid, Rm‖Rc‖ROLE‖ULEN‖USERNAME)、K1/K2 = HMAC(SIK, 0x01/0x02 ×20)、RAKP2/3 authcode = HMAC(Kuid, Rc‖SIDm‖ROLE‖ULEN‖USERNAME)、RAKP4 ICV = HMAC(SIK, Rm‖SIDc‖GUIDc)[:12]、完整性覆盖 AuthType..Next Header（4 字节对齐）、机密性 = IV(16)‖AES-CBC-128(K2[:16])。测试双保险：推导向量独立用 Python hashlib 复算钉死；假 BMC 按同协议实现服务端跑通「握手 → 加解密 → 电源命令」全流程，并验证密码错误在 RAKP2 处失败、篡改包被拒（ErrIntegrity）。

验证：`gofmt` / `go build ./...` / `go vet ./...`、`go test ./internal/ipmi/` 与 `npx vue-tsc --noEmit` 全部通过。

### 10.35 IdcsmartSale 插件（业务经理 / 销售系统）（本轮补齐）

CBAP 包 `addon/IdcsmartSale.zip` 的 PHP 全部 ionCube 加密，前端 `template/admin`（七个页面 + api/sales.js + 三语语言包）与大额订单邮件模板可读。此前统计页已有（§10.8 说明），本轮补齐业务经理维度（049 迁移）：

| 维度 | 参考实现（前端契约） | ShitIDC 落地 |
|---|---|---|
| 销售成员 | `GET/POST/PUT/DELETE /sale`、`/sale/:id/{enable,disable}`：姓名 / 编号 / 邮箱 / 邀请链接 | `sales` 表挂后台账号（admin_id 唯一），姓名 / 编号 / 邮箱 / 启停；邀请链接未落地（站内无对应注册归因口径，与推介计划 `?ref=` 互不混淆） |
| 用户绑定 | `/sale/client` 五件套 + 未绑定用户列表 | `sale_clients`（user_id 唯一，一人归一个销售，换绑先解除）；EntityPicker 选用户 |
| 提成规则 | 全局提成设置（首充 / 续充 fixed·percent）+ 商品提成设置（product[] + new/renew/repurchase/upgrade 四类 mode/value） | `sale_commission_configs`：global 一条兜底 + product 级覆盖（unique(scope, product_id)，保存即 upsert）；fixed 落分、percent 落基点 |
| 大额订单奖励 | money_data：min_price / max_price / mode / value + big_order 邮件模板 | `system_settings.sale` 的 big_min_cents / big_max_cents（0=不限）/ mode / value；支付成功命中区间记一条 big_order 提成。邮件通知未单独接（OrderPaid 事件可经 §10.25 的邮件通知配置发信） |
| 确认天数 | confirm_wait_day | 提成记录落库时写 confirm_at = now + N 天；到期惰性转 active（列表与汇总实时计算），管理员可置 invalid |
| 记提成 | 支付成功按规则归属销售 | 支付收尾接线 `AccrueSaleCommission`：renewal→renew、upgrade→upgrade、new 按客户是否买过同商品区分 new / repurchase；商品级规则优先于全局；幂等键 `订单:明细:类型` |
| 统计 | `/sale/statistics`、`/sale/order_ranking`、`/sale/client_ranking`、`/sale/commission`、导出 Excel | `/admin/sale/statistics`（每销售：订单数 / 销售额 / 已生效与待确认提成）、`/admin/sale/client-ranking`（消费总金额排名）、`/admin/sale-commissions`（提成详情：类型 / 基数 / 方式 / 金额 / 状态筛选与置无效）；导出走站内「导出中心」模式，未单独实现 |

未落地项（前端契约不足 + 服务端加密，不编造）：任务奖励（task 列表仅见名称，无字段）、充值提成（首充 / 续充挂在充值流程上，插件逻辑加密）、邀请链接。这些类型在提成口径中不存在，不会静默按 0 处理。

验证：`gofmt` / `go build ./...` / `go vet ./...` 与 `npx vue-tsc --noEmit` 全部通过。

### 10.36 EContract 插件（电子合同）（本轮补齐）

CBAP 包 `addon/EContract.zip` 的 PHP（controller / logic / model）ionCube 加密，前端 `template/admin`（contract_config / contract_template / create_contract_template / contract_details / e_contract）与 `template/clientarea`（applyContract / signContract / contractDetail）完整可读。§10.13 曾因「EContract 插件加密」整体跳过；按 §10.15 的思路以可见契约重新落地（050 迁移）：

| 维度 | 参考实现（前端契约） | ShitIDC 落地 |
|---|---|---|
| 基础设置 | switch 功能开关 / day_limit 申请时间限制 / 我方单位名 / 社会信用代码 / 联系人 / 电话 / 邮箱 / 地址 / 邮编 / 合同编号前缀与起始编号（20 位以内数字自动递增）/ logo / 公司印章 / 指纹信息 | `system_settings.e_contract` 同名语义；编号只能向前推进；指纹信息未落地（站内签订无指纹采集设备口径） |
| 模板 | 名称 / 内容 / 关联商品 / 基础合同 / 强制签订 / 状态 / 备注 / 变量清单 / 复制 | `e_contract_templates` 同名语义 + `GET` 返回变量清单（合同编号 / 我方信息 / 客户 / 订单金额 / 商品名 / 时间等 14 个）；复制默认停用 |
| 申请 | 用户对已支付订单申请合同（`/e_contract/order` 列表 + apply） | 功能开关 → 时间窗 → 一单一有效合同 → 按订单商品选启用模板（回退基础合同）→ 编号（前缀 + 递增号，行锁防重号）→ 变量渲染快照落库 |
| 签订 | signContract（用户签字，可上传签名图） | 用户端「合同」页手写签名板（canvas → PNG dataURL ≤200KB）→ 状态 pending → signed |
| 审核 | 通过（complete）/ 驳回（reject，理由必填）/ 作废（cancel，原因必填） | `POST /admin/e-contracts/:id/review`，仅 pending / signed 可处理 |
| 邮寄 | `POST /:id/mail`：快递公司 + 单号 | 仅已生效合同可登记，展示在列表 |
| 下载 | `POST /:id/download`（PDF） | 可打印 HTML（含 Logo / 印章 / 客户签名 / 正式版式），已驳回 / 作废不可下载；PDF 需内嵌 CJK 字体，纯标准库不可行（有意差异，见 README） |

有意差异：插件对接的第三方电子签通道（逻辑加密）不落地，签订为站内流程（签名图 + 审核留痕）；合同内容为申请时快照，此后修改模板 / 设置不影响历史合同。

验证：`gofmt` / `go build ./...` / `go vet ./...` 与 `npx vue-tsc --noEmit` 全部通过。

### 10.37 NoticeSendMerge 插件（通知合并发送）——明确跳过

CBAP 包 `addon/NoticeSendMerge.zip` 共 11 个文件：主类 / model / route / 三语 lang 全部 ionCube 加密，`template/admin` 与 `template/clientarea` 只有空目录（.gitkeep），README.md 内容为「插件样式Demo」五个字。**没有任何可读契约**——接口路径、字段面、合并策略一概不可见，连「合并」的对象（站内信？邮件？按用户还是按事件？）都无从判断。

按 §7.2「识别不了的不编造」原则：不为一个名字发明一套行为。若后续拿到该插件的明文版本或文档，可按站内已有的通知基础设施（`client_care_mails` 收件箱、internal/notify、邮件通道注册表）套用同样的「前端字段面 → 存储层 → 管理页」路径补齐。

至此 §10.15 审计的 CBAP `addon/` 30 个插件全部收口：28 个已对齐（含本轮 5 个），IdcsmartDomain 按 §9.4 跳过（协议加密且涉及真实域名扣费），NoticeSendMerge 按 §10.37 跳过（零契约）。

### 10.38 product_divert 插件（用户自助产品转移）（本轮补齐）

主程序包 `zjmf-finance/public/plugins/addons/product_divert/` 的 PHP（controller / model / validate / lang）ionCube 加密，但 `config/config.php`（状态字典）、`menu.php` / `menuclientarea.php`、`README` 与全部模板（后台 setting / index，用户端 pushpulllist / pushserver / pullserver）可读——此前 §10.8 仅按「主类加密」整体跳过，按 §10.15 的思路以可见契约重新落地（051 迁移）。与 §10.26 的管理员产品转移（HostTransfer）互补：这是**用户对用户**的自助转移。

| 维度 | 参考实现（模板契约） | ShitIDC 落地 |
|---|---|---|
| 基础配置 | is_open 启用开关 / validity_period 转出有效期（天，超时未接受自动关闭）/ push_cost 转出费用 / pull_cost 转入费用 / protection_period 订购保护期（订购后多久才能转移）/ product_range[] 可自助转移的产品范围（多选） | `system_settings.product_divert`；`GET/PUT /admin/product-divert/config`（service.manage + CSRF + 审计），后台「自助转移」页表单；金额落「分」 |
| 发起转出 | pushserver 模态：选产品 + 按手机号或邮箱查接收方（账号前半 + 星号脱敏展示）+ 展示转出费用 | `POST /product-divert/lookup`（精确匹配 email / phone，返回脱敏账号）、`POST /product-divert`（校验：启用、非本人、非已删除服务、保护期、商品范围、无待接收转移）；用户端「产品转移」页发起 |
| 转出费用 | `push_pay_status` + `payamount(push_invoice_id)`，支付后接收方收到转入通知 | 费用 > 0 时生成 `kind='artificial'`、`kind_detail='divert_push'` 的人工订单（订单 + 账单 7 天到期）；钱包 / 在线支付 / 管理员标记支付三条路径在收尾钩子 `advanceDivertFeeOrderTx` 里标记已付并给接收方发站内通知 |
| 接收 / 转入 | 接收方列表在「push 已付且待接收」时可见「接收 / 拒绝」；接收后支付转入费用，「支付后，该产品会立刻转移到您的账户中」 | `POST /product-divert/:id/accept`：转出费用未付时拒绝；转入费用 > 0 生成 `divert_pull` 费用单，= 0 立即完成；转入费用支付完成（同一钩子）即迁移产品归属并通知双方 |
| 状态字典 | config.php：1 待接收 / 2 已完成 / 3 已关闭 / 4 已拒绝 | 同字典（SMALLINT 原值落库，页面同文案） |
| 取消 / 拒绝 | pushrefuse（转出方取消，已付后仍可取消）/ pullrefuse（接收方拒绝） | `POST /product-divert/:id/cancel`（转出方，状态 → 3）/ `:id/reject`（接收方，状态 → 4）；两者都作废**未支付**的费用订单与账单 |
| 超时关闭 | validity_period 气泡注释「超过该时间未接受的转出，将会被自动关闭」 | 调度器 @every 5m `ExpireProductDiverts`：过期待接收 → 状态 3 + 作废未支付费用单 |
| 转移列表 | pushpulllist：产品（name + domain + ip）/ 对方 / 我方费用 / 发起时间 / 完成时间 / 类型（转出·转入）/ 状态 / 操作（支付 / 取消 / 接收 / 拒绝 / 手动检测），状态筛选与分页 | 用户端 `/divert`「产品转移」页同列布局；对方账号掩码；`GET /product-divert`（status 1~4 过滤）；后台「自助转移」页看全量记录（完整邮箱） |
| 手动检测 | verificationResult（pull 已支付后的兜底核对入口） | `POST /product-divert/:id/verify`：双方费用已支付且仍未完成时补一次迁移，幂等 |
| 迁移留痕 | 插件留 product_divert 记录 | 迁移完成同时写 `service_transfers`（备注「用户自助转移」），后台「产品转移」记录页可见 |

口径说明（加密代码不可读，按可见契约的最保守解释实现）：

- 转移完成只迁移**所选服务**的归属（`services.user_id`），订单 / 账单 / 支付记录不迁移——插件推送模态只展示单个产品，没有 HostTransfer 的「关联产品自动迁移」语义；
- 已支付的费用在拒绝 / 取消 / 超时关闭时**不自动退还**（插件模板没有任何退款语义）；未支付的费用订单与账单一并作废，需要退款走管理员既有退款流程；
- 查找接收方按手机号 / 邮箱**精确匹配**且返回脱敏账号，不提供模糊搜索（防用户枚举）；
- product_range 不选视为「全部商品可自助转移」（表单语义无更多线索，页面提示写明）；
- 双方费用各自生成人工订单：转出方付转出费用、接收方付转入费用，费用为 0 的那一侧免支付直接放行。

验证：`gofmt` / `go build ./...` / `go vet ./...` / `go test ./internal/...` 与 `npx vue-tsc --noEmit` 全部通过。

### 10.39 expired_auto_delete_bill 插件（到期账单处理）（本轮补齐）

主程序包 `zjmf-finance/public/plugins/addons/expired_auto_delete_bill/` 的主类与语言包 ionCube 加密，但 `menu.php`（设置 / 账单处理记录两个菜单项）与全部模板（setting.tpl / index.tpl）可读——此前 §10.8 仅按「主类加密」整体跳过，按 §10.15 的思路以可见契约重新落地（052 迁移）。

| 维度 | 参考实现（模板契约） | ShitIDC 落地 |
|---|---|---|
| 处理方式 | setting.tpl 唯一字段：产品到期账单处理方式——无（空）/ delete 直接删除 / cancel 标记取消 | `system_settings.expired_auto_delete_bill`；`GET/PUT /admin/expired-bill-action`（invoice.manage + CSRF + 审计），后台「到期账单处理」页单选 |
| 行为 | 产品到期（删除）时把其未支付账单删除或标记取消 | worker terminate 收尾（`RecordExpiredIPLog` 旁）调用 `RecordExpiredBillAction`：把该服务未支付的**续费**订单（kind='renewal'，经 orders.renew_service_id）与账单作废——到期后服务已不可续费，账单不可能再被支付，作废是账面上最忠实的等价动作 |
| 记录 | index.tpl「账单处理记录」：账单号（Cancelled 带链接）/ 状态（着色）/ 处理时间 / 关联产品（domain + dedicatedip） | `expired_bill_logs` 表逐笔留档（账单公开 ID / 处理后状态 void / 配置的 action / 服务与产品名 / 实例主 IP / 用户）；`GET /admin/expired-bill-logs`（关键词 + 分页），后台同页展示 |
| 权限 | 插件语言包加密，权限点不可读 | 统一 `invoice.manage`（账单属财务数据，与「发票管理」同权限） |

有意差异（2 处，均为账目口径）：

- **「直接删除」与「标记取消」的最终账面状态相同（void）**：站内账目不物理删除（§10.23 口径：订单 / 账单 / 支付记录不物理删除），插件「删除」是物理删行；本实现两种方式都作废订单与账单，配置里选的动作原样记进日志的 action 列，差异保留在记录里而不是账面上；
- **只处理续费账单**：插件处理「产品到期账单」；站内已终止服务的未支付账单只有续费单（新购账单随订单走，人工单与发票费用单有各自的支付/作废流程），故范围限定为 `kind='renewal'` 的未支付订单 / 账单。

未配置（action 为空）时钩子是空操作，默认行为与从前完全一致；处理失败只记日志，不影响终止本身（与到期 IP 记录同一口径）。

验证：`gofmt` / `go build ./...` / `go vet ./...` / `go test ./internal/...` 与 `npx vue-tsc --noEmit` 全部通过。
