# ShitIDC API 使用文档

ShitIDC 对外提供两套入口：

| 入口 | 地址前缀 | 用途 |
| --- | --- | --- |
| 系统 API | `/api/v1/...` | 用户/自动化脚本/下游程序以账号身份操作：下单、支付、查服务、管工单、改资料 |
| 魔方兼容层（当上游） | `/compat/magiccube/v1/...` | 让另一套魔方财务（或其它售卖面板）把 **ShitIDC 当作上游** 来连接 |

机器可读的 OpenAPI 规范在 [`docs/openapi.yaml`](openapi.yaml)，站点部署后可通过 `/docs/openapi.yaml` 直接访问。

---

## 1. 认证

两种认证方式，按请求头区分：

### 1.1 API Token（程序调用用这个）

在用户中心「账户 → API 管理」创建 Token，返回形如：

```text
shitidc_a1b2c3d4e5f60718.9f8e7d6c5b4a3210fedcba9876543210
```

它由 `KeyID.Secret` 组成，**Secret 只显示一次**。之后每个请求带上：

```http
Authorization: Bearer shitidc_a1b2c3d4e5f60718.9f8e7d6c5b4a3210fedcba9876543210
```

- Token 权限是创建时勾选权限的**子集**，遵循最小授权：只想让它查产品就只勾 `product.read`。
- Token 不需要 CSRF 头（CSRF 只约束浏览器 Cookie 会话）。
- 可选设置过期时间；泄露后立刻在「API 管理」撤销。
- Secret 在服务端只存 HMAC，数据库拖走也无法还原。

### 1.2 Cookie Session（浏览器）

登录后由 `shitidc_session` Cookie 承载，写操作必须带 `X-CSRF-Token` 请求头（登录响应和 `GET /api/v1/auth/me` 会返回 `csrf_token`）。程序集成请一律使用 API Token。

---

## 2. 响应格式

统一信封：

```json
{ "data": { }, "error": null, "request_id": "uuid" }
```

出错时 HTTP 状态码非 2xx，`error` 为：

```json
{ "code": "INSUFFICIENT_BALANCE", "message": "余额不足" }
```

常见错误码：`UNAUTHORIZED`（未登录/Token 无效）、`FORBIDDEN`（Token 缺少所需权限 scope）、`RATE_LIMITED`、`NOT_FOUND` 类、`INSUFFICIENT_BALANCE`。

限流：默认每 IP 每分钟 `RATE_LIMIT_PER_MINUTE` 次（见 `.env`），批量脚本请自行控制速率并处理 429。

---

## 3. 权限 scope 与端点对照

| scope | 端点 | 说明 |
| --- | --- | --- |
| `product.read` | `GET /api/v1/products` | 在售产品与周期价格 |
| `order.read` | `GET /api/v1/orders`、`POST /api/v1/orders`、`POST /api/v1/orders/{id}/cancel`、`POST /api/v1/services/{id}/renew` | 订单查询/下单/取消/生成续费订单 |
| `wallet.read` | `GET /api/v1/wallet`、`GET /api/v1/wallet/transactions`、`POST /api/v1/orders/{id}/pay`、`POST /api/v1/orders/{id}/pay/online`、`POST /api/v1/wallet/recharge`、`GET /api/v1/payment-methods` | 余额、流水、支付 |
| `invoice.read` | `GET /api/v1/invoices`、`GET /api/v1/invoices/{id}` | 账单列表与明细 |
| `service.read` | `GET /api/v1/services`、`GET /api/v1/services/{id}` | 服务列表、详情与开通配置 |
| `ticket.read` | `GET /api/v1/tickets`、`GET /api/v1/tickets/{id}` | 工单列表与会话 |
| `ticket.write` | `POST /api/v1/tickets`、`POST /api/v1/tickets/{id}/reply`、`POST /api/v1/tickets/{id}/close` | 提交/回复/关闭工单 |
| `profile.manage` | `GET /api/v1/profile`、`PUT /api/v1/profile` | 个人资料 |
| `api_token.manage` | `GET/POST /api/v1/api-tokens`、`DELETE /api/v1/api-tokens/{id}` | 管理 Token 自身 |

**其他免授权 / 公开端点：**

| 端点 | 说明 |
| --- | --- |
| `GET /api/v1/products/{id}/prices` | 产品的可选计费周期与价格档 |
| `GET /api/v1/product-groups` | 产品分组（公开） |
| `GET /api/v1/announcements` | 站内公告（公开） |
| `GET /api/v1/auth/captcha` | 获取图形验证码 `{id, svg}`；配置 Redis 后注册/登录/发码/找回密码强制校验 |
| `POST /api/v1/auth/2fa/setup|enable|disable`、`GET /api/v1/auth/2fa/status` | TOTP 两步验证自助管理 |
| `GET /api/v1/sessions`、`DELETE /api/v1/sessions/{id}`、`POST /api/v1/sessions/revoke-others` | 登录设备管理 |
| `POST /api/v1/pay/notify/{method}`、`GET /api/v1/pay/return/{method}` | 支付回调，按渠道经支付注册表分发（旧地址 `/pay/epay/notify` 仍可用） |

> 客服人员的 `ticket.manage`、管理员的 `wallet.adjust` / `product.write` / `provider.manage` 等属于后台权限，不会出现在普通用户的可勾选列表里，也不要签进自动化 Token。

---

## 4. 快速上手示例

```bash
BASE=https://你的域名
TOKEN=shitidc_xxx.yyy          # 创建 Token 时完整保存的那串

# 1. 查看在售产品（拿 product_id）
curl -s -H "Authorization: Bearer $TOKEN" $BASE/api/v1/products

# 2. 下单（月付 1 件）
curl -s -X POST -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{"product_id":"<产品UUID>","billing_cycle":"monthly","quantity":1}' \
  $BASE/api/v1/orders

# 3. 余额支付（服务进入异步开通）
curl -s -X POST -H "Authorization: Bearer $TOKEN" $BASE/api/v1/orders/<订单UUID>/pay

# 4. 查询服务与到期时间
curl -s -H "Authorization: Bearer $TOKEN" $BASE/api/v1/services

# 5. 生成续费订单并支付（到期时间自动顺延一个周期）
curl -s -X POST -H "Authorization: Bearer $TOKEN" $BASE/api/v1/services/<服务UUID>/renew
curl -s -X POST -H "Authorization: Bearer $TOKEN" $BASE/api/v1/orders/<续费订单UUID>/pay

# 6. 提交并跟进工单
curl -s -X POST -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{"subject":"服务器 502","priority":"high","message":"实例 xxx 从 3 点开始 502"}' \
  $BASE/api/v1/tickets
curl -s -H "Authorization: Bearer $TOKEN" $BASE/api/v1/tickets/<工单UUID>
curl -s -X POST -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{"body":"补充：重启后恢复了一段时间又复现"}' \
  $BASE/api/v1/tickets/<工单UUID>/reply
```

下单幂等：支付接口支持 `Idempotency-Key` 头，网络重试时传同一个值可避免重复扣款。

---

## 5. 把 ShitIDC 当作上游（下游面板对接）

这是"自己成为上游"的入口。典型场景：你用 ShitIDC 管理资源和结算，前端再挂一层魔方财务/其它售卖面板做分销；或者给大客户开子面板，让他们用 API 采购你的资源。

### 5.1 给下游发凭证

在 ShitIDC 后台为下游专门注册一个账号，用该账号创建一个 API Token，按需勾选权限：

- `product.read`：拉取商品目录（必需）
- `order.read`：开通资源（`/create`，从该账号余额扣费）
- `service.operate`：暂停 / 解除暂停 / 续费 / 终止

### 5.2 下游魔方财务配置

在下游魔方的「上游对接 / 接口」里填：

| 下游配置项 | 填什么 |
| --- | --- |
| 接口地址 / base_url | `https://你的ShitIDC域名` |
| 上游账号 | 任意标识（仅记录用，如 `reseller-a`） |
| API Key / 密码 | 你的完整 API Token（`shitidc_xxx.yyy` 整串） |

下游会先调用：

```http
POST /compat/magiccube/v1/login_api
Content-Type: application/x-www-form-urlencoded

account=reseller-a&password=shitidc_xxx.yyy
```

返回（兼容魔方登录响应的多种字段位置，下游取哪个都能取到）：

```json
{ "status": 200, "msg": "success", "jwt": "shitidc_xxx.yyy", "token": "shitidc_xxx.yyy", "data": { "jwt": "shitidc_xxx.yyy" } }
```

这个 `jwt` 就是同一个 API Token，下游随后用 `Authorization: Bearer <jwt>` 请求商品接口：

```http
GET /compat/magiccube/v1/products
```

也就是说：**登录换发的不是新会话，就是原 Token 本身**——ShitIDC 不引入第二套凭证体系，撤销 Token 即可立刻断开某个下游。

### 5.3 扩展方向

- 资源操作路由已补齐：`POST /create`（开通，扣余额，`Idempotency-Key` 防重复）、`POST /suspend|unsuspend|terminate|renew`（`{"id": 服务ID}`）。契约与本系统自带的魔方客户端对称：create 请求体 `{request_id, product_ref, user_id, options}`，`options.billing_cycle` 可选（默认 monthly）；renew 会创建续费订单并立即从余额结算顺延到期。不同魔方版本适配器的字段名可能有差异，正式对接前先用测试 Token 走一遍 开通→暂停→续费→终止。
- 非魔方面板可以直接走 `/api/v1/orders` 标准接口完成"查价 → 下单 → 支付 → 查服务"全流程。
- ShitIDC 自己作为下游去连魔方的部分见使用教程第 10 节；两个方向互不影响——同一套 `providers` 表管"我们的上游"，兼容层管"别人把我们当上游"。

---

## 6. 客服工单流程（站内）

1. 用户在「工单」提交，系统立即给所有持 `ticket.manage` 权限的账号发邮件通知（SMTP 未配置时跳过，站内不受影响）。
2. 客服登录后进入 **管理后台 → 工单客服**：只需 `ticket.manage` 权限，看不到商品/支付/供应商等其它管理功能。
3. 客服回复后：工单转为「已回复待确认」，用户收到邮件通知；用户再次回复会自动把工单转回「待处理」并通知客服。
4. 用户或客服都可以关闭工单；用户对已关闭工单的回复会自动重新打开。
