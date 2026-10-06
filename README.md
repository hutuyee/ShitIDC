# ShitIDC

一个按“Core / Provider / Compatibility / Theme / Worker”拆分的 ShitIDC 财务系统 MVP 源码，技术栈为 Go + Gin + PostgreSQL + Redis + Vue 3 + TypeScript。

## 已实现

- 用户注册、登录、Session、CSRF、Argon2id 密码哈希
- 自定义 SMTP + 注册邮箱验证码：强制验证开关、发送频率/错误次数限制、未验证邮箱禁止登录，防批量注册与爆破
- RBAC 权限系统
- 个人中心：用户自助维护联系信息与详细资料（昵称/手机/QQ/真实姓名/公司/国家省市/地址）
- 工单会话：用户与客服双向回复、状态流转（待处理/已回复/已关闭）、邮件通知双方；**客服工单台只需 `ticket.manage` 权限**，不需要开放整个管理后台
- 产品、周期价格、订单、账单
- 服务续费：到期前一键生成续费订单，余额/在线支付成功后到期时间自动顺延，不重复开通实例
- 钱包账户与不可删除的资金流水
- 余额支付：事务、行锁、幂等键、重复支付保护
- 易支付（Epay）在线支付：MD5 签名跳转收银台、回调验签/金额校验/幂等入账，支持订单支付与钱包充值
- 订单中心：展示购买商品、配置、数量、金额、下单时间与支付方式；未支付订单可取消，24 小时未支付自动关闭
- 顺序用户编号（UID）：第一个注册用户为 1 依次递增，管理后台按 UID 调账
- 服务状态机与 Redis/Asynq 异步开通
- 服务生命周期管理：管理后台暂停/解除暂停/终止（经队列调用上游 Provider），到期服务自动暂停，可选 `AUTO_TERMINATE_SUSPENDED_DAYS` 自动终止暂停过久的服务，卡在过渡态的任务由 Scheduler 自动补偿重投
- Hook 事件总线（`internal/events`）：注册/登录/下单/支付/续费/暂停/工单等核心事件统一发布，为扩展系统预留接缝
- 出站 Webhook：事件按订阅推送到第三方，HMAC-SHA256 签名 + 时间戳防伪造/防重放，经队列异步投递、失败自动重试，后台可管理并查看投递记录
- 退款冲正：钱包支付订单退款以“冲正交易”方式入账，原始流水永不修改，数据库幂等键防重复退款
- 认证安全补全：修改密码（吊销全部会话）、邮箱验证码找回密码、登录尝试逐条留痕（user_login_logs）、失败 10 次持久化锁定、安全事件（security_events）、魔方迁移旧密码（MD5）首次登录自动升级 Argon2id
- 邮件发送入队（mail.send 任务），验证码/工单通知不再阻塞请求；provider.sync 定时同步任务真实实现
- PII 脱敏：后台查看用户资料默认打码，`pii.read.full` 权限可见全量且操作写入审计日志
- API Token 独立限流（`TOKEN_RATE_LIMIT_PER_MINUTE`）、`/api/v1/version` 版本端点、`/docs/api-docs.html` 交互式 API 文档
- 财务核心测试：并发扣款（20 并发扣 10 元仅 10 成功）、支付回调幂等、后端价格重算、退款冲正账本一致性（`TEST_DATABASE_URL` 启用）
- 备份/恢复脚本（`scripts/backup.sh`、`scripts/restore.sh`）、nginx TLS/HSTS/接口限流配置、结构化日志与访问日志
- API Token（Secret 仅显示一次、HMAC 保存）+ 完整 API 使用文档（`docs/api.md`，内置到前端 API 管理页）+ OpenAPI 规范
- 兼容层双向：既能作为下游连魔方上游，也提供 `/compat/magiccube/v1/*`（login_api / products / create / suspend / unsuspend / terminate / renew）让别的魔方/面板把 ShitIDC 当上游——create/renew 从下游 Token 所属用户余额扣费，Idempotency-Key 防重复开通
- 管理后台：用户列表（UID/邮箱/验证状态/余额/订单数/最近活跃）、行为与审计日志（登录/下单/支付/调账全留痕、可过滤）、钱包调账、审计日志
- 用户中心重做为顶部导航 + 双栏仪表板：资源、账单、工单、余额、最近订单与消费趋势
- 前端管理界面覆盖全部新能力：服务生命周期（暂停/恢复/终止/重试）、Webhook 管理（密钥仅显示一次 + 投递记录）、订单退款表单、用户禁用与脱敏资料查看、个人中心改密、登录页找回密码
- 持久化供应商管理：上游网站、账号、加密 API Key、连接状态、同步时间
- MagicCube Provider：默认支持 `/v1/login_api` 换取 Token、`/v1/products` 同步商品，接口路径可按版本覆盖
- 上游商品映射表与一键导入，本地商品自动记录供应商和上游 Product ID
- `/compat/magiccube/v1/products` 兼容入口基础
- MagicCube MySQL 迁移工具：Dry Run、字段映射 Profile、报告
- CSS 变量主题系统，附 default / dark 两套主题
- Docker Compose、Nginx、API/Worker/Scheduler 分离，Scheduler 会补偿重新投递 pending 服务并自动关闭超时未支付订单
- API 请求日志（`api_logs`，路线图 §5.6 审计三表补全）：中间件异步记录方法/路径/状态码/耗时/错误码，后台可按方法、路径、状态过滤
- 图形验证码与人机验证通道（§9 必须项）：注册、登录、发送邮箱验证码、找回密码全部强制校验；内置纯标准库 SVG 数学验证码 + Redis 存储（未配置通道时回退使用）；还支持可插拔第三方通道（谷歌 reCAPTCHA / 腾讯云验证码），后台可增删通道、凭据加密入库
- 会话/设备管理（§8）：用户可查看全部活跃登录会话（IP/User-Agent/最近活跃）、单独下线某台设备、一键下线其他设备
- TOTP 两步验证（§9 可选 2FA）：纯标准库 RFC 6238 实现（通过 RFC 测试向量），密钥 AES-256-GCM 加密存储，登录强制校验验证码，找回密码同时重置 2FA 防锁死
- 异常 IP 检测（§9）：账户从历史未用过的 IP 成功登录时写入安全事件
- 产品分组（第五阶段）：分组 CRUD、商品归属分组、产品中心按分组区块展示；管理端商品列表支持编辑、价格调整、上下架
- PaymentProvider 抽象（第七阶段）：支付渠道接口 + 注册表，易支付作为第一个实现，新增支付宝/微信/Stripe 只需注册新 Gateway，回调按渠道分发（`/api/v1/pay/notify/:method`）
- 站内公告：管理后台发布/置顶/下线公告，用户仪表板展示真实公告（替换原硬编码占位内容）
- 账单详情与服务详情：账单明细行 + 订单状态；服务实例展示上游开通返回的配置信息
- 产品多周期购买：下单弹窗可选计费周期（按后端价格档）与数量，后端下单时重算价格
- 登录日志 / API 日志后台查询页、订单中心（全站订单 + 退款记录）、暗色模式下 Naive UI 组件主题适配、前端 404 路由
- **优惠系统**：fixed/percent 优惠券，事务内原子核销（全局总量、每人限用、最低消费、商品范围、有效期），下单弹窗验证并显示优惠
- **推广系统**：注册绑定邀请码、`/referral` 页生成专属链接，订单支付后自动按比例返佣入余额（数据库幂等键防重复结算），比例后台可调
- **代理系统**：用户组整体折扣（先组折扣、后优惠码），代理分组管理页一键分配用户
- **更多支付**：支付注册表新增 **Stripe**（Checkout Session + webhook 签名验证（校验 payment_status=paid）+ Refund API）、**支付宝官方**（RSA2 + 异步验签 + 自动退款）、**微信支付 APIv3**、**PayPal**、**USDT（Epusdt）**、**虎皮椒**（MD5，响应验签修正参考实现的恒真笔误）、**GoAllPay**（AllPay v5 SHA256，一个实现覆盖支付宝/微信/银联）、**OCGC 酷云**（RSA 双向验签的两步会话式下单）、**支付宝国际支付**（境外收单 create_forex_trade，MD5 签名 + 汇率换算结算），均通过纯标准库实现并附带签名自洽测试
- **自动退款**：管理端退款入口统一分发——钱包支付走冲正交易，Stripe/支付宝订单自动调用网关退款 API 原路退回
- **更多 Provider**：新增 **Proxmox VE**（API Token 认证，LXC/QEMU 克隆开通、暂停/恢复/删除）、**Virtualizor**（HMAC 签名管理 API）、**NOKVM 虚拟化**（值排序 MD5 签名，开通/暂停/改配）与 **kangle 虚拟主机**（`md5(a+token+r)` 签名，续费=解除暂停）供应商，worker 自动分发
- **WASM 扩展系统（Extension SDK）**：wazero 宿主 + 能力权限 ABI v1（log / storage / http，逐条强制授权），扩展包 zip 上传、启停、日志查看；参考扩展 `extensions/demo-logger`（Go wasip1 `//go:wasmexport` 实现）+ `scripts/build-extension.sh`，宿主测试真实加载并分发事件
- **Theme SDK / 主题上传**：theme.json + variables.css 契约文档（`docs/theme-sdk.md`），主题 zip 上传安装（Zip Slip、压缩炸弹、文件类型白名单防护）、激活与删除
- **财务统计**：`/admin/stats` 营收（今日/7天/30天）、MRR 估算、退款/充值汇总、30 天营收趋势图、热销商品、订单状态分布
- **通知中心**：核心事件自动生成站内通知，顶栏铃铛 + 未读角标 + 一键已读
- **邮件模板**：验证码/密码重置/登录提醒/工单通知五类模板后台可编辑，`{{占位符}}` 渲染，留空回退内置文案
- **工单附件**：5MB 上限、扩展名 + 魔数双校验、随机文件名、下载强制 attachment 语义、对象级授权
- **多币种**：货币表 + 汇率管理（基础货币 CNY），商店设置可配置基础货币
- **CSV 导出**：管理端一键导出用户 / 订单 CSV
- **附属插件对齐（魔方 addon 与 gateways）**：**到期产品删除IP记录**（服务终止成功后留档主 IP / 分配 IP 与开通时间，后台可搜索）与**数据导出至 Excel**（导出中心：自定义列表 + 字段选择 + 时间区间，纯标准库生成 xlsx；内置「已支付账单」「推广业绩（推广佣金口径）」两个数据集）；另对齐 gateways/**user_custom 线下支付**——渠道配置填写收款说明（支持 HTML），用户下单后原样展示，管理员在订单中心「确认收款」后自动开通（余额充值不支持线下支付）
- **待办事项 widget（魔方 widget/ToDo 对齐）**：管理控制台按权限聚合待处理工单、待审实名认证、开通中产品数量，点击直达对应页面；本轮同时补上**实名审核页**（通过 / 驳回并写明原因），把之前只有 API 没有界面的审核流程补齐。
- **kangle V10 模块（魔方 sub_server/kanghostx 对齐）**：面板协议与 wlkanglepro 同构，商品配置新增 kanghostx 键位支持（`way` + `parameter1..16` 自定义 / `kl_*` 弹性），带宽按插件口径 M×128 换算，改配跟随 `way` 读取对应键位
- **btHost 虚拟主机（魔方 servers/bthosts 与 V10 bthostx 对齐）**：新增原生 `bthosts` Provider——上游 API 为 `/api/vhost/*`（签名 = time/random/accesshash 排序后 MD5 大写），支持开通（user_create + host_build）/ 暂停 / 启用 / 删除（回收站）/ 续费同步到期 / 升降级 / 限速联动；商品配置同时兼容经典键位（`type` + `plans_id`/`site_max` 等）与 V10 键位（`way` + `parameter1..20` / `bt_*`，流量 G→MB、带宽 MB/s→KB/s 换算）
- **基础风控**：每小时下单频次限制（可配置）+ 超限安全事件留痕；`MASTER_KEY_FILE` 支持 Docker Secrets 挂载密钥（KMS-lite）
- **魔方财务（ZJMF）双向对接**：既能作为下游连上游魔方，也能作为**上游**被魔方财务当资源接口使用——附 `zjmf-plugin/` 服务器模块，装到魔方 `public/plugins/servers/` 后即可在**魔方的商品管理界面**里把商品绑定到 ShitIDC，实现自动开通/暂停/解除/删除/续费。签名沿用魔方明文模块的 `time+random+token → md5 大写` 规则（`internal/apisign` 含与 PHP 语义逐字对照的测试），接口密钥以 `MASTER_KEY` 加密存储、5 分钟防重放、逐密钥作用域与停用。详见 [docs/zjmf-integration.md](docs/zjmf-integration.md)
- **商品描述完全由管理员手写**：前台原样展示（保留换行），不再自动拼接"系统提取的卖点"，留空时也不显示占位文案
- **商品配置项（可配置选项）**：与魔方一致的四类——下拉 / 单选 / 开关 / 数量，子项带相对加价与一次性初装费；下单页按类型渲染并实时显示加价，**计价只在服务端进行**（`ResolveConfigSelection` 是唯一入口，未知子项、跨项引用、超范围数量一律拒绝），配置项分组可挂到多个商品
- **商品自定义字段**：text / textarea / dropdown / password，支持必填、正则校验、仅管理员可见、是否在下单页展示；只接受商品声明过的字段键，不会把任意键值塞进开通请求
- **库存与限购**：`stock_control` + 库存数、是否允许一次买多件、单客户最大购买数；库存扣减与订单在同一事务，超卖在事务层面不可能发生
- **邮箱验证码注册**：未开启"强制邮箱验证"时验证码为可选——填了就必须正确（填对直接标记邮箱已验证，省掉登录后再验证一次），不填则照常注册、登录时再验证
- **计费模型（对齐魔方 pay_type）**：周期付费 / 一次性 / 免费 / 试用四种类型；计费周期扩展到小时 / 天 / 月 / 季 / 半年 / 年 / 一次性，一个商品可同时提供多个周期。**免费与 0 元试用订单金额为 0 时直接结算并开通、不走支付网关**；试用到期由 Scheduler 自动回收；开通后 N 天自动删除可选
- **商品管理后台**：计费类型、多周期价格、配置项编辑器（下拉/单选/开关/数量 + 候选项加价与初装费）、自定义字段编辑器（含字段键、正则、可见性）、库存与限购，全部在一个页面里完成
- **升降级**：按剩余天数折算当前方案剩余价值（与 WHMCS / 魔方同一算法），差价为 0 或负数立即生效、为正则生成升级订单；付款后由 worker 调用上游 `ChangePackage` 真实改配
- **配置项条件联动**：选 A 才显示并必填 B；被联动隐藏的配置项**即使前端提交了值也不计费**，报价无法被绕过
- **微信支付 APIv3**：Native 下单 + 平台证书 RSA 验签（含 5 分钟防重放）+ AES-256-GCM 回调解密 + 原路退款，纯 Go 标准库实现并附带真实密钥往返测试
- **按量 / 超量计费**：磁盘与流量各有包含额度、硬上限与单价；**已出账水位**保证同一段用量只收一次钱（重复出账为 0 元）。用量只增不减防抖动，超过硬上限标记待暂停由调度器执行
- **宝塔面板 Provider**：建站 / 停用 / 启用 / 删除 + 用量读取，签名与魔方 `bthosts` 模块逐字节一致（附与 PHP 写法对照的测试 + 出站请求签名可复算的测试）
- **多币种独立定价**：同一商品在每个币种上可以有自己的价格（**不用汇率折算**），下单按 `(商品, 周期, 币种)` 精确取价；钱包按币种隔离，人民币钱包付不了美元订单。指定了商品没有价格的币种会明确报错，而不是悄悄回退用错价格
- **短信通道与验证码**：可插拔的短信通道抽象（已实现阿里云 / 腾讯云（TC3 签名手写）/ 赛邮 / 华为云（WSSE 口径与魔方插件逐字节一致）/ 短信宝 / 第二办公室 / 布丁云 v10 / 通用短信宝式 / 通用 HTTP 模板通道，关键签名均有独立向量测试）；验证码与邮箱验证码同构（哈希存储、限频、错 5 次锁定、10 分钟过期），另外按手机号做 1 分钟/1 小时/1 天三级风控并落发送流水，防止平台被当成短信轰炸跳板
- **邮件通道**：对应魔方 `public/plugins/mail/`，从「只能配 SMTP」扩展为可插拔通道（阿里云邮件推送（RPC 签名与短信同源）/ 赛邮邮件 / 宝塔邮局 / 通用 HTTP 模板），凭据加密入库、后台可增删/设默认/发测试邮件；**通道优先，未配置通道时回退内置 SMTP**，老部署升级后行为不变
- **人机验证通道**：对应魔方 `public/plugins/captcha/`，在内置图形验证码之上扩展可插拔第三方通道（谷歌 reCAPTCHA / 腾讯云验证码）：后端只校验浏览器拿到的票据（谷歌走独立校验接口，腾讯云走 TC3-HMAC-SHA256 签名，`internal/tc3` 与短信共用同一套签名实现），前端按 `/auth/captcha` 返回的通道类型渲染组件；通道不可用或未配置时原样回退内置图形验证码
- **对象存储通道**：对应魔方 `public/plugins/oss/`（TencentcloudOss），可插拔对象存储抽象（腾讯云 COS v5 签名，纯标准库）：配置后**工单附件上传即转存对象存储**（图片 / PDF 按插件口径 public-read，其余 private）、下载 302 到 3 分钟签名地址，本机不再留档；未配置通道时附件仍存本机，行为与升级前一致。后台可增删 / 设默认 / 测试连接（HEAD 桶）
- **商品返现**：对齐魔方 CBAP `product_cashback` 插件：后台按商品配置返现规则（固定金额、可返现期限 0=永久），支付成功后自动返到买家余额（min(规则金额, 实付)，同一订单只返一次，币种随订单流水）
- **成本支出**：对齐魔方 CBAP `cost_pay` 插件：在订单里登记支出（名称 / 所属主体 / 金额 / 支出日期 / 备注），支持自定义字段（文本框 / 下拉、必填、列表展示、上移下移排序）与关键词 / 主体 / 时间区间筛选；按币种汇总今日 / 本月 / 今年支出（对应插件看板 widget）。入口在订单中心每行「成本支出」或侧栏「成本支出」，权限 `finance.report`
- **异常巡查记录**：对齐魔方 CBAP `abnormal_inspection_records` 插件：巡查发现的异常情况留档（关联用户及其名下产品、异常时 IP、异常事项 / 处理措施 / 处理时间、多张异常截图点击可放大），支持关键词（用户 / 公司 / 联系方式 / IP）与处理时间筛选、Excel 导出；截图存本机 `uploads/inspection/`，配置对象存储通道后自动转存并走签名地址。权限 `service.manage`
- **客户自定义字段**：对齐魔方 CBAP `client_custom_field` 插件：后台定义用户详情可输入的信息（文本框 / 下拉 / 链接 / 密码 / 勾选框 / 文本区 / 下拉文本框，支持下拉值、描述、正则校验、必填、订购前必填、管理员可见、注册时显示、显示状态与排序），用户在个人中心填写、注册页可一并提交（注册时显示的字段），管理员在用户详情查看（密码只显示是否已设置，订购前必填缺失时禁止下单）。入口：侧栏「用户字段」与用户列表详情页签，权限 `user.read`
- **商品购买限制**：对齐魔方 CBAP `product_cert_limit` / `product_cycle_limit` / `product_related_limit` 插件：商品可配「实名要求」（未实名禁止下单）、「周期性限购」（N 天内最多拥有多少件，0=永久，周期以未受限的第一单起算）、「关联限购」（捆绑 = 须同单一起结算 / 必需 = 需先拥有激活中的关联商品 / 互斥 = 不得拥有）；下单时服务端强制校验（单品 / 购物车 / 续费全链路）。`product_num_limit` 与商品自带的「单客户最多购买」等价，沿用商品配置。入口：侧栏「商品限购」，权限 `product.write`
- **代金券**：对齐魔方 CBAP `IdcsmartVoucher` 插件：后台创建代金券（券码 8 位含大小写与数字，券码/类型/面额创建后不可改），公开券用户可在前台领取、私有券由后台按用户或全部用户发放（可设总量与每人一次）；下单 / 续费 / 升降级时定额抵扣，抵扣不超过应付金额、不找零、订单取消不返还，并受最低金额 / 适用商品 / 需先拥有的激活商品 / 计费周期 / 用户类型 / 仅限续费或升级等限制。入口：用户中心「代金券」与后台侧栏「代金券」，权限 `voucher.manage`
- **活动促销**：对齐魔方 CBAP `EventPromotion` 插件：后台创建活动（百分比折扣 / 满减、参与商品、指定用户、新注册用户 / 现有客户 / 单用户一次 / 周期限制、生效与截止时间、置顶置底排序），满足条件的订单在下单时自动享受折扣、无需用户填码；同一订单只应用排序最靠前的一个活动，金额由服务端重算（优惠券、代金券在其后叠加）。入口：侧栏「活动促销」，权限 `promotion.manage`
- **周期人工订单**：对齐魔方 CBAP `CycleArtificialOrder` 插件：后台创建生成规则（用户、订单描述、订单金额 / 续费金额、时间范围、生成周期 num + 天 / 月 / 年），调度器每 10 分钟为到期周期生成人工订单（首次按订单金额、之后按续费金额；幂等、可补生成、单轮每规则上限 30 笔）；详情页支持筛选子订单、调整价格（同步订单 / 明细 / 账单）、标记支付（可优先扣除余额，余下记为线下收款）与删除 / 批量删除。人工订单支付后不开通服务，超时自动取消明确跳过人工订单。入口：侧栏「周期人工订单」，权限 `cycle_order.manage`
- **发票管理**：对齐魔方 CBAP `IdcsmartInvoice` 插件：用户端维护发票抬头（公司 / 个人、普票 / 专票）与收件地址（纸质 / 电子），勾选已支付订单（可选预开票的未支付订单、跨年开关）申请开票并试算税金与快递费，税金 / 快递费生成人工费用单、支付后进入审核；后台审核通过 / 驳回、上传电子发票（pdf / ofd / xml / zip）、登记纸质发票快递单号与快递单照片、查看 / 下载发票文件与冲红，另可维护发票开关、发票项目（普票 / 专票税率与收税比例）与用户抬头 / 地址。入口：用户中心「发票」与后台侧栏「发票管理」，权限 `invoice.manage`
- **邮件通知**：对齐魔方 CBAP `EmailNoticeAdmin` 插件：后台为每个业务动作（注册 / 登录 / 订单 / 支付 / 服务 / 工单等 20 个事件）配置邮件接口、邮件模板与通知人员并开关；事件发生后由事件总线异步给选定员工发通知邮件，可按规则指定发信通道，失败不影响业务流程。入口：侧栏「邮件通知」，权限 `email_notice.manage`
- **产品自助转移**：对齐魔方主程序附属插件 `product_divert`：用户在「产品转移」页把名下产品转给另一个用户——按手机号 / 邮箱精确查找接收方（脱敏确认），转出方支付转出费用、接收方确认并支付转入费用后产品立刻迁移（可分别配置费用、转出有效期、订购保护期与可转移商品范围，超时未接收自动关闭并作废未支付费用单）；双方费用走人工订单，钱包 / 在线支付 / 管理员标记支付都会推进转移状态，转移完成在「产品转移」记录页留痕（备注「用户自助转移」）。与后台代操作的产品转移（HostTransfer）互补。入口：用户中心「产品转移」与后台侧栏「自助转移」，权限 `service.manage`
- **产品转移**：对齐魔方 CBAP `HostTransfer` 插件：后台在「服务管理」或「用户详情 → 机器」把产品转移到另一个用户名下（选目标用户 + 备注，二次确认），同一订单的关联产品会一起迁移，订单 / 账单 / 支付记录不迁移；每次迁移留记录，「产品转移」页支持关键词查询。入口：侧栏「产品转移」，权限 `service.manage`
- **流量包**：对齐魔方 CBAP `FlowPacket` 插件：后台「流量包」页维护流量包（名称 / 流量GB / 售价 / 可用库存与库存开关 / 关联商品多选 / 备注）并启停，查看流量包订单（未付款 / 已付款 / 已取消 / 已退款）与删除记录；用户端「流量包」页为名下适用产品下单，余额支付（余额不足保留待付款订单，可在页内支付或取消），付款后广播 `service.updated`（kind=flow_packet_paid）。入口：侧栏「流量包」与用户中心「流量包」，权限 `flow_packet.manage`
- **客户关怀**：对齐魔方 CBAP `ClientCare` 插件：后台「客户关怀」页按条件圈人（指定用户 / 注册时长 / 上次登录 / 产品数量 / 产品状态 / 购买与删除时间 / 指定产品 / 指定接口）创建推送任务（站内信或短信+邮件、一次性 / 每天 / 每周 / 每月、同用户重复发送开关），支持预览推送名单、启停与删除；调度器每 1 分钟执行到期任务，把站内信写进用户收件箱并发站内通知，邮件按指定通道经队列投递（短信自定义内容暂不投递）；用户中心「消息」页查看站内信（未读 / 已读、上一篇 / 下一篇）。入口：侧栏「客户关怀」与用户中心「消息」，权限 `client_care.manage`
- **内部工单**：对齐魔方 CBAP `TicketInternalPremium` 插件：后台「内部工单」页新建 / 接单 / 回复（含预设回复）/ 转单 / 处理完成 / 关闭 / 评分与筛选查询，工单配置维护部门与类型（处理时限）/ 状态 / 预设回复 / 其他设置，并提供定时工单（每 N 天 / 自然月 / 年 + 日期范围 + 触发时间）与工单统计（评分角色 / 范围 / 单量与处理时长 / 评分与超时占比、部门与个人排名）；超时按处理时限判定、剩余不足 15% 站内提醒。入口：侧栏「内部工单」，权限 `ticket_internal.manage`
- **工单高级版**：对齐魔方 CBAP `TicketPremium` 插件：用户端提交工单可选部门 → 类型（处理时限）、关联产品，展示工单须知；后台「工单管理」页按关键词 / 类型 / 状态 / 用户 / 跟进人 / 领取人 / 时间范围筛选并自动刷新，支持代客建单、接单（可要求先接单才能回复）、仅领取人回复、处理完成（可同时关闭）、关闭 / 重开与转内部工单；工单详情含沟通记录、内部备注、预设回复、操作日志与回复附件；用户在详情页催单（15 分钟限一次）并在处理完成后对满意度 / 服务态度 / 处理时效三项 0.5–5 星评分；「工单配置」页维护部门 / 状态（完结状态自动计入已处理）/ 预设回复 / 接单与须知设置，「工单统计」页给出单量、处理时长、评分与超时占比及部门 / 个人排名；建单 / 回复 / 关闭邮件走 `ticket_client_create`、`ticket_client_reply`、`ticket_admin_reply`、`ticket_client_close` 四个内置模板。入口：侧栏「工单客服 / 工单配置 / 工单统计」与用户中心「工单」，权限 `ticket.manage`

- **推介计划**：对齐魔方 CBAP `IdcsmartRecommend` 插件：用户在会员中心「推广」页开启推介计划后获得专属推介链接（含自定义后缀链接）与建议推介产品，被推介用户购买商品支付成功后按商品「新购 / 续费」两套比例生成奖励记录（触发最低金额按同一订单相同商品合计金额计算），经确认天数（0=即刻确认，默认 14 天）自动转为已确认并可申请提现；后台「推介计划」页支持按推介人 / 被推介人 / 商品 / 状态筛选，改奖励金额、确认、冻结 / 解冻、置无效（预设回复或自定义原因）、删除与提现审核（通过 / 驳回 / 已打款），「推介配置」页维护奖励存款、确认天数、最低提现金额、提现手续费、默认推介链接与商品奖励比例、主打推介产品；奖励到账写站内通知并走 `recommend_notice` 邮件模板。与旧的按订单总额即时返佣互斥：推荐人开启过推介计划时走本模块，避免同一订单重复发放。入口：侧栏「推介计划 / 推介配置」与用户中心「推广」，权限 `wallet.adjust`

- **商品下拉优化**：对齐魔方 CBAP `ProductDropDownSelect` 插件：后台「商品下拉优化」页四张样式卡（平铺 / 一级分组 / 二级分组 / 一级 + 二级分组）选择服务升降级弹窗里商品下拉框的下拉样式，分组视图按商品分组聚合实时预览；用户端「服务 → 升降级」按配置渲染（站内商品分组只有一级，三种分组样式呈现相同，样式值按插件原样保存）。入口：侧栏「商品下拉优化」，权限 `product.write`
- **万云资源**：对齐魔方 CBAP `WanyunResource` 插件：后台「万云资源」页五页签的机房资源台账——IP 段（父子两级子网 / 分组 / IP 数 / 可用已用 / 地址明细：分配人 / 使用人 / 使用单位 / 已用状态与分配时间）、节点（类型 + 自定义字段：文本框 / 下拉、必填、信息展示、拖动排序）、VLAN（类型 / 编号 / 分配人 / 使用人 / 使用单位 / 途径节点 / 启停）、光纤与纤芯（编号 / 所属 / 芯数 / 开通与施工单位 / 联系人 / 项目 / 价格 / 途径节点 + 纤芯自定义字段 + 补芯）。插件的 DCIM 接口同步依赖加密协议，数据全部手工维护。入口：侧栏「万云资源」，权限 `wanyun_resource.manage`
- **手动资源**：对齐魔方 CBAP `ManualResource` 插件：后台「手动资源」页维护独立服务器台账（主 IP / 附加 IP / 配置 / 成本 / 系统账密 / 控制方式 ipmi 或客户端 / IPMI IP 端口版本与控制账密 / DCIM 客户端地址与服务器 ID / 到期时间 / 备注）与供应商（名称 / 联系方式 / 备注），资源可分配到客户名下产品（按服务选择，客户随服务带出）或置回空闲；IPMI 模式支持开机 / 关机 / 重启 / 电源状态（纯标准库 IPMI 2.0 / RMCP+ 客户端：RAKP-HMAC-SHA1 + HMAC-SHA1-96 + AES-CBC-128，密钥推导与帧格式按 ipmitool lanplus 对齐，固定向量 + 假 BMC 端到端测试钉死），结果落最近电源状态。VNC 控制台与重装 / 救援 / 破解密码依赖加密的 DCIM 客户端协议，明确不支持。入口：侧栏「手动资源」，权限 `manual_resource.manage`
- **业务经理**：对齐魔方 CBAP `IdcsmartSale` 插件：后台「业务经理」页四页签——成员管理（销售挂后台账号，姓名 / 编号 / 邮箱 / 启停）、用户绑定（一人归一个销售）、提成设置（全局 + 商品级规则：新购 / 续费 / 复购 / 升降级，固定金额或比例；大额订单奖励区间 + 确认天数）、统计（销售汇总：订单数 / 销售额 / 已生效与待确认提成；消费总金额排名；提成详情与置无效）。支付成功自动按规则记提成：续费单取续费、升级单取升降级、新购单按客户是否买过同商品区分新购 / 复购，商品级规则优先于全局，幂等键挡重复，确认天数后生效。插件的任务奖励与充值提成前端契约不足，不编造。入口：侧栏「业务经理」，权限 `sale.manage`
- **电子合同**：对齐魔方 CBAP `EContract` 插件：后台「电子合同」页维护合同模板（{{变量}} 渲染、关联商品、基础合同、强制签订、复制）、审核用户申请（通过 / 驳回 / 作废）、邮寄登记（快递公司 + 单号）、下载合同文件与基础设置（功能开关 / 申请时间限制 / 我方单位信息 / 合同编号前缀与起始编号 / Logo 与印章）；用户中心「合同」页对已支付订单申请合同（编号自动递增、一单一合同、时间窗限制）、手写签名板签订、查看与下载。有意差异：插件对接的第三方电子签通道加密不可读，签订为站内流程；合同文件为可打印 HTML（含印章与签名）。入口：用户中心「合同」与后台侧栏「电子合同」，权限 `e_contract.manage`
- **接口分组与容量分配**：商品可绑定接口分组，开通时按 `least_loaded` / `fill_first` / `round_robin` 策略挑一个还有容量的接口；容量按「还活着的服务」计算，分组满了会明确失败并提示扩容，而不是硬塞到已满的接口上
- **PayPal**：Orders v2 下单，access token 缓存（3 笔订单只取 1 次），回调验签走官方 `verify-webhook-signature` 接口；金额用字符串解析避免浮点误差
- **USDT（Epusdt）**：签名算法与官方文档例子**逐字节一致**（测试直接复算文档给出的签名），回调验签 + 法币金额换算
- **实名认证**：通道可插拔（**阿里云二要素**、**支付宝芝麻认证**、**智简魔方芝麻信用**、**微信人脸核身（腾讯云慧眼）**、**银行卡二/三/四要素**、**手机号三要素**、**涪擎实名认证（高级版）**、**E证通人脸认证（腾讯云）**、人工审核），管理端可增删与切换默认通道；扫码类通道提交后展示二维码并每 3 秒自动轮询结果，中途刷新可继续查询。**真实姓名与身份证号绝不落明文**——只存掩码与 `HMAC-SHA256` 指纹（裸哈希可被穷举反查）；本地先校验身份证校验位（含"2 月 30 日"这类被 `time.Date` 归一化放过的非法日期），挡掉打错一位再调付费通道；同一证件号不能认证两个账号。两处对参考实现的纠偏：支付宝响应经 RSA2 验签后才采信（否则伪造响应可把账号刷成已认证）；扫码通道「未完成 / 查询异常」一律按处理中返回，不照插件那样直接判失败
- **后台不再要求手抄 UUID**：任何需要引用用户/商品的地方都是「点开输入框 → 搜索 → 看到关键信息 → 选中」（新组件 `web/src/components/EntityPicker.vue` + 后端 `/admin/search` 统一搜索，支持邮箱、UID、UUID、商品名模糊匹配）
- **点击用户行查看全部信息**：抽屉里一次看到余额、机器、订单、授信与占用，可直接调账、查看实名资料、禁用账号（`/admin/users/:id/detail` 聚合接口，避免前端发四五个请求还要处理部分失败）
- **公告可以点开看全文**：仪表板只放最新 5 条做入口（整行可点、摘要单行截断），点开进 `/announcements/:id` 详情页按段落渲染全文；`/announcements` 列表页支持搜索与只看置顶。下线的公告对用户返回 404，撤下的内容不会被翻出来
- **后台公告管理独立成页**：撰写区左编辑右实时预览（公告的排版就是它的全部价值），列表整宽展示、支持搜索
- **后台路径可配置**：后端 `ADMIN_PATH`（默认 `/admin-panel`，`/admin` 始终作为兼容镜像）+ 前端 `VITE_ADMIN_PATH`，两处独立可调
- **后付费（授信赊账）**：授信**显式授予**（默认关闭，注册不会自动获得赊账能力）。额度占用实时计算不存冗余字段；下单时在事务内 `FOR UPDATE` 锁用户行串行判断，**并发下单不会超额度**（测试：额度够 3 单时并发 10 笔恰好成功 3 笔）；有欠款时不允许停用授信；额度变更全部留痕便于复盘坏账
- **购物车多商品结算**：购物车**不存价格**（每次读取都用下单同一套逻辑重算，组折扣/专属价/下架立刻反映）；一次结算生成一个批次，订单仍一笔一单；合并付款**整批一次扣款**且与下单时的约定金额比对，价格被改动就停下来让用户确认，绝不按旧价静默扣款。商城前台已接入：商品页「加入购物车」→ 购物车页改数量 / 移除 / 优惠码结算，余额可整批一次支付，也可到订单页逐笔支付
- **第三方登录**：标准授权码流程 + 通道可插拔（已实现 GitHub / QQ / 微信（unionid 透传）/ 微博 / 支付宝（与支付共用 RSA2 签名 kit）/ 钉钉（authCode 归一化）/ Google / 企业微信（服务商应用，含 suite_ticket 指令回调解密入库））。state 一次性存库且**取出即删**（防 CSRF 与重放，并校验 state 所属通道）；跳转地址白名单过滤（拒绝 `//evil.com`、`/\evil.com` 等 9 种开放重定向写法）；第三方身份**不能被静默改绑**到别人账号；无密码账号解绑时必须至少留一种登录方式
- **客户组按产品差异定价**：除了全局组折扣，还支持「某组 + 某商品 + 某周期 + 某币种」的专属固定价；专属价**不再叠加组折扣**，且折扣**不作用于配置项加价与初装费**（成本项打折会亏）

## 重要说明

现在不需要在创建商品时手工填写一个孤立的“魔方上游产品 ID”。管理员进入 **管理后台 -> 供应商 / 上游**，填写上游魔方财务的网站地址、你在上游的账号和 API Key；保存后依次点击 **测试连接 -> 同步产品 -> 导入商品**。ShitIDC 会把供应商、上游 Product ID 和本地商品关系写入数据库。

**反过来把 ShitIDC 当魔方上游**：同一个页面下方的「作为魔方财务的上游」面板里生成接口密钥，把地址与 Hash 填进魔方后台的 **接口设置**，再把 `zjmf-plugin/shitidc`（或 `zjmf-plugin/shitidc-module.zip`）放到魔方的 `public/plugins/servers/` 下，魔方商品管理的「接口类型」里就会出现 **ShitIDC**。完整步骤、接口清单与排错表见 [docs/zjmf-integration.md](docs/zjmf-integration.md)；与魔方财务的功能差异见 [docs/zjmf-diff.md](docs/zjmf-diff.md)。

> 说明：魔方财务自身的 openapi（`app/openapi/*`）**全部经 ionCube 加密**（355/355 个 PHP 文件），路径与签名无法从源码确认，因此本项目不去猜测它，而是走魔方**明文可读的服务器模块**约定，两端协议都归本项目所有。

### 邮箱验证码（注册）

1. **管理后台 → 设置 → 邮件（SMTP）** 填好 SMTP 主机、端口、发件邮箱与密码，并保存。
2. 打开 **强制邮箱验证** 开关（在同一页）：注册必须填邮箱验证码，且**未验证邮箱无法登录**。
   不打开也可以：只要 SMTP 配好，注册页就会出现"邮箱验证码（可选）"，用户填了就必须正确。
3. 验证码为 6 位数字、**10 分钟**有效、同一邮箱**60 秒**内不可重复发送；发送与校验都要过图形验证码。
4. 登录页在未验证时会自动切到"验证邮箱"标签页，重发验证码即可。

邮件模板可在 **管理后台 → 邮件模板** 里改（`email_verification`，支持 `{{code}}` / `{{email}}` 占位符）。

### 与魔方财务的功能对齐进度

ShitIDC 的目标是**功能上对齐魔方财务，但全部用 Go 实现**——魔方靠 PHP 插件做的事，
在这里是编译进单一二进制的 Go 接口实现（`internal/provider` 对应它的 server 插件，
`internal/payment` 对应它的 gateway 插件）。已对齐、本轮新增、以及仍待补齐的差距
（免费/试用/一次性商品、更多计费周期、升降级、短信、第三方登录、宝塔 Provider 等）
按优先级列在 [docs/parity-roadmap.md](docs/parity-roadmap.md)。

默认 MagicCube 兼容预设使用 `/v1/login_api`（账号 + API Key）换取 Token，再读取 `/v1/products`。不同魔方版本的自动开通/暂停/续费/删除接口仍可能不同，所以这些动作路径保留为供应商“高级兼容设置”，不会把某个版本未经确认的路径写死进 Core。商品同步和供应商管理可以直接使用默认预设；要自动开通资源，需要按你实际上游版本填写对应资源 API 动作路径并先测试。

迁移引擎已支持用户、产品、钱包余额、订单、账单、支付和服务；但示例 Profile 只提供用户/产品示范，因为不同魔方版本表结构并不固定。其余实体必须由你按实际数据库写 SELECT 映射，系统不会猜字段。正式迁移后报告中的 `reconciliation` 段会自动对比源库与目标库的用户/产品/订单/账单/支付/服务数量及钱包总额，任何差异都需要人工核对后再切换。迁移进来的旧密码（魔方 MD5）保留在 `legacy_password_hash`，用户第一次登录验证成功后自动升级为 Argon2id，无需强制重置。

### 从旧版 ShitIDC 更新

如果 PostgreSQL 数据卷已经存在，Docker 的 `docker-entrypoint-initdb.d` **不会自动重新执行新迁移**。升级本版本后手动执行：

```bash
docker compose exec -T postgres sh -lc 'psql -U "$POSTGRES_USER" -d "$POSTGRES_DB"' < migrations/003_provider_upstream.sql
docker compose exec -T postgres sh -lc 'psql -U "$POSTGRES_USER" -d "$POSTGRES_DB"' < migrations/004_orders_payments_email.sql
docker compose exec -T postgres sh -lc 'psql -U "$POSTGRES_USER" -d "$POSTGRES_DB"' < migrations/005_profile_tickets_renewal.sql
docker compose exec -T postgres sh -lc 'psql -U "$POSTGRES_USER" -d "$POSTGRES_DB"' < migrations/006_lifecycle_webhooks_security.sql
docker compose exec -T postgres sh -lc 'psql -U "$POSTGRES_USER" -d "$POSTGRES_DB"' < migrations/007_mvp_gaps.sql
docker compose exec -T postgres sh -lc 'psql -U "$POSTGRES_USER" -d "$POSTGRES_DB"' < migrations/008_v2_features.sql
docker compose exec -T postgres sh -lc 'psql -U "$POSTGRES_USER" -d "$POSTGRES_DB"' < migrations/009_magiccube_upstream.sql
docker compose exec -T postgres sh -lc 'psql -U "$POSTGRES_USER" -d "$POSTGRES_DB"' < migrations/010_config_options.sql
docker compose exec -T postgres sh -lc 'psql -U "$POSTGRES_USER" -d "$POSTGRES_DB"' < migrations/011_billing_types.sql
docker compose exec -T postgres sh -lc 'psql -U "$POSTGRES_USER" -d "$POSTGRES_DB"' < migrations/012_upgrades.sql
docker compose exec -T postgres sh -lc 'psql -U "$POSTGRES_USER" -d "$POSTGRES_DB"' < migrations/013_long_cycles_links.sql
docker compose exec -T postgres sh -lc 'psql -U "$POSTGRES_USER" -d "$POSTGRES_DB"' < migrations/014_metered_billing.sql
docker compose exec -T postgres sh -lc 'psql -U "$POSTGRES_USER" -d "$POSTGRES_DB"' < migrations/015_sms.sql
docker compose exec -T postgres sh -lc 'psql -U "$POSTGRES_USER" -d "$POSTGRES_DB"' < migrations/016_provider_groups.sql
docker compose exec -T postgres sh -lc 'psql -U "$POSTGRES_USER" -d "$POSTGRES_DB"' < migrations/017_user_product_prices.sql
docker compose exec -T postgres sh -lc 'psql -U "$POSTGRES_USER" -d "$POSTGRES_DB"' < migrations/018_certification.sql
docker compose exec -T postgres sh -lc 'psql -U "$POSTGRES_USER" -d "$POSTGRES_DB"' < migrations/019_oauth.sql
docker compose exec -T postgres sh -lc 'psql -U "$POSTGRES_USER" -d "$POSTGRES_DB"' < migrations/020_cart.sql
docker compose exec -T postgres sh -lc 'psql -U "$POSTGRES_USER" -d "$POSTGRES_DB"' < migrations/021_postpaid.sql
docker compose exec -T postgres sh -lc 'psql -U "$POSTGRES_USER" -d "$POSTGRES_DB"' < migrations/022_custom_providers.sql
docker compose exec -T postgres sh -lc 'psql -U "$POSTGRES_USER" -d "$POSTGRES_DB"' < migrations/023_mail_providers.sql
docker compose exec -T postgres sh -lc 'psql -U "$POSTGRES_USER" -d "$POSTGRES_DB"' < migrations/024_oauth_suite_tickets.sql
docker compose exec -T postgres sh -lc 'psql -U "$POSTGRES_USER" -d "$POSTGRES_DB"' < migrations/025_captcha_providers.sql
docker compose exec -T postgres sh -lc 'psql -U "$POSTGRES_USER" -d "$POSTGRES_DB"' < migrations/026_certification_provider_ref.sql
```

如果你的 shell 没有导出这两个变量，可直接用 `.env` 里的实际用户名和数据库名替换。全新数据库会按 `001 -> 013` 自动执行。

所有迁移都已用真实 PostgreSQL 验证过**可重复执行**（连续跑两遍不报错），这也是它们全都写成
`CREATE TABLE IF NOT EXISTS` / `ADD COLUMN IF NOT EXISTS` / `DO $$ ... EXCEPTION WHEN duplicate_object` 的原因。

> `009` 除了建魔方对接表，还会补一次 `UPDATE coupons SET product_ids='{}' WHERE product_ids IS NULL`，
> 把历史遗留的 NULL 优惠券范围修正为空数组（等价于全场通用）。

## 目录

```text
cmd/server       HTTP API
cmd/worker       异步任务
cmd/scheduler    定时任务
cmd/migrate      魔方数据库迁移 CLI
internal/api     API / Middleware
internal/store   PostgreSQL 事务与业务数据访问
internal/provider Provider 抽象
internal/security 密码、加密、SSRF 防护
themes           主题包
web              Vue 3 前端
migrations       PostgreSQL Schema / Seed
migration        魔方迁移 Profile
zjmf-plugin      魔方财务服务器模块（把 ShitIDC 当魔方上游）
docs             API / 主题 / 魔方对接与差异分析文档
deploy           Nginx
```

## 快速启动

见 `使用教程.md`。

## 品牌自定义

默认站点名称为 `ShitIDC`。项目不会强制绑定固定 Logo：

1. 把头像/Logo 放到 `web/public/`，例如 `web/public/logo.png`。
2. Docker 部署时在根目录 `.env` 设置：

```env
VITE_SITE_NAME=ShitIDC
VITE_SITE_LOGO_URL=/logo.png
```

3. 本地运行 Vue 时，把 `web/.env.example` 复制为 `web/.env` 后修改同样两个变量。

`VITE_SITE_LOGO_URL` 留空时，会自动显示站点名称首字母作为占位头像，因此仓库可以直接发布到 GitHub，不需要预置你的私人 Logo。

Go Module 已统一为 `github.com/hutuyee/ShitIDC`。如果将来把仓库迁移到其他 GitHub 用户名或组织名，需要同时修改 `go.mod` 和源码中的本地 import 路径。

## Go 依赖锁文件

仓库第一次在可联网的 Go 环境中使用时，建议在项目根目录执行：

```bash
go mod tidy
```

这不会编译程序，只会整理 `go.mod` 并生成/更新 `go.sum`。随后把 `go.mod` 和 `go.sum` 一起提交到 GitHub，以便后续构建可复现。

Docker 构建也会在复制源码后执行一次 `go mod tidy`，因此新仓库即使暂时还没有 `go.sum`，只要构建环境能够访问 Go Module 代理，也不会再因为缺少 `go.sum` 校验项而直接失败。
