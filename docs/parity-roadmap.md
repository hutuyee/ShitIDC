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
- pending_invoices 待处理发票：魔方 IdcsmartInvoice 指发票（开票 / 寄送）流程，ShitIDC 的 invoice 是账单（unpaid/paid/void），语义不同。

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
| ClientCare | 客户关怀：邮件/站内信、周期推送、指定用户 | 未落地 |
| ClientCustomField | 客户自定义字段（管理列表/申请） | 已对齐（§10.19） |
| CostPay | 支出记录：来源/主体/金额/日期 | 已对齐（§10.17） |
| CreditLimit | 授信：消费记录、混合支付 | 已对齐（授信账户 + 后台授信管理） |
| CycleArtificialOrder | 人工订单：调价、批量、子项调价 | 已对齐（§10.23） |
| EContract | 电子合同：模板/签署/邮寄 | 未落地（§10.13 已声明跳过） |
| EmailNoticeAdmin | 管理员邮件通知：接口+模板+收件人 | 部分（邮件通道/模板已有；事件通知管理员未落地） |
| EventPromotion | 促销：满减/百分比、时间窗 | 已对齐（§10.22） |
| FlowPacket | 流量包管理 | 未落地 |
| HostTransfer | 主机转移 | 未落地 |
| IdcsmartClientLevel | 客户等级：三级、商品可选、批量保存 | 已对齐（客户组差异定价，口径等价） |
| IdcsmartDomain | 域名 | 跳过（§9.4） |
| IdcsmartInvoice | 开票申请：抬头/快递/邮寄/驳回 | 未落地（站内「发票」指账单口径） |
| IdcsmartRecommend | 推荐/关联商品：分组、排序、复制 | 未落地（商品页仅有「推荐商品」标记） |
| IdcsmartSale | 销售统计：消费排名、时间窗图表 | 部分（统计页已有；业务经理维度见 §10.8 说明） |
| IdcsmartStatistics | 统计图表 | 已对齐（后台统计/仪表盘） |
| IdcsmartVoucher | 代金券：发放/使用/次数 | 已对齐（§10.21） |
| IdcsmartWebhook | 消息推送（钉钉/企业微信等） | 已对齐（internal/webhook + 后台 Webhook 页） |
| ManualResource | 手动资源：供应商、noVNC 控制台 | 未落地 |
| NoticeSendMerge | 通知合并发送 | 未落地 |
| ProductCashback | 商品返现 | 已对齐（§10.16） |
| ProductCertLimit | 产品实名限制 | 已对齐（§10.20） |
| ProductCycleLimit | 购买周期限制 | 已对齐（§10.20） |
| ProductDropDownSelect | 商品下拉选择（线索不足） | 未落地（前端仅「商品选择」） |
| ProductNumLimit | 购买数量限制 | 已对齐（商品自带单客户限购） |
| ProductRelatedLimit | 关联购买限制 | 已对齐（§10.20） |
| TicketInternalPremium | 工单内部备注/内部工单 | 未落地 |
| TicketPremium | 工单高级版（部门/字段/回执模板） | 部分（基础工单已有） |
| WanyunResource | 万云资源：自定义字段、节点 | 未落地 |

主程序包 `zjmf-finance/public/plugins/addons/` 的 5 个（demo_style 示例、expired_ip_log / export_excel 已对齐见 §10.8、expired_auto_delete_bill 与 product_divert 主类 ionCube 加密）本轮复核无变化。

说明：未落地项的共同原因是「服务端加密 + 无对外契约」；其中多数（FlowPacket、EventPromotion、Product* 限制系列、HostTransfer）站内已有可复用的骨架（商品/订单/结算/事件），后续可逐个按其前端资产可见的字段面直接设计实现，无需参考加密代码。

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
