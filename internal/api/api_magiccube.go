package api

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/hutuyee/ShitIDC/internal/apisign"
	"github.com/hutuyee/ShitIDC/internal/events"
	"github.com/hutuyee/ShitIDC/internal/httpx"
	"github.com/hutuyee/ShitIDC/internal/security"
	"github.com/hutuyee/ShitIDC/internal/store"
)

// 魔方（ZJMF / 智简魔方）上游对接协议。
//
// 魔方财务的服务器模块（接口插件）通过固定协议调用上游面板：请求体带
// time / random / signature，签名算法抄自魔方财务 3.7.6 的明文参考模块
// public/plugins/servers/bthosts/bthosts.php:14-23：把 time、random、token
// 三个值按字符串排序拼接后取 md5，再转大写。token 从不随请求发送：魔方把
// 它填在“接口设置”的 Hash(accesshash) 字段里，对应 ShitIDC 的上游访问密钥。
//
// 验签需要密钥原文，而用户级 api_tokens 只保存 HMAC（无法反推），所以魔方
// 对接单独用 upstream_keys 表，密钥以 MASTER_KEY 做 AES-256-GCM 加密存储。
//
// 响应统一使用魔方约定信封：code=1 成功，其它值为失败且 msg 会直接显示在
// 魔方后台的任务日志里。

const (
	upstreamKeyCtx    = "upstream_key"
	upstreamSecretCtx = "upstream_key_secret"
	upstreamTolerance = 5 * time.Minute
)

// upstreamFail 返回魔方错误信封。业务失败仍用 200 + code，认证失败才用 401/403，
// 方便运维在访问日志里区分“配置错误”和“业务拒绝”。
func upstreamFail(c *gin.Context, status int, msg string) {
	if status >= 200 && status < 300 {
		status = http.StatusOK
	}
	c.JSON(status, gin.H{"code": status, "msg": msg, "data": nil})
}

func upstreamOK(c *gin.Context, msg string, data any) {
	if data == nil {
		data = gin.H{}
	}
	c.JSON(http.StatusOK, gin.H{"code": 1, "msg": msg, "data": data})
}

// requireUpstreamKey 校验魔方模块请求。模块用 curl 发 x-www-form-urlencoded，
// 所以表单和查询串都接受（部分魔方版本会用 GET 做轻量探活）。
func (a *App) requireUpstreamKey() gin.HandlerFunc {
	return func(c *gin.Context) {
		_ = c.Request.ParseForm()
		var bodyToken string
		if c.Request.Body != nil && strings.HasPrefix(c.ContentType(), "application/json") {
			var body struct {
				Token string `json:"token"`
			}
			if err := c.ShouldBindBodyWithJSON(&body); err == nil {
				bodyToken = strings.TrimSpace(body.Token)
			}
		}
		get := func(k string) string {
			if v := c.Request.FormValue(k); v != "" {
				return v
			}
			if v := c.Query(k); v != "" {
				return v
			}
			if k == "token" {
				return bodyToken
			}
			return ""
		}
		// The Hash(accesshash) field carries "<key_id>.<secret>". The secret is
		// only used to recompute the digest; the key id identifies the row. The
		// bare key id is accepted too so a hand-typed value still works.
		raw := strings.TrimSpace(get("token"))
		if raw == "" {
			upstreamFail(c, http.StatusUnauthorized, "缺少接口密钥：请在魔方后台“接口设置”的 Hash 字段填入 ShitIDC 生成的上游访问密钥")
			c.Abort()
			return
		}
		keyID := raw
		if head, secret, ok := strings.Cut(raw, "."); ok {
			keyID = strings.TrimSpace(head)
			_ = secret
		}
		if a.Store == nil {
			upstreamFail(c, http.StatusServiceUnavailable, "服务未完成初始化")
			c.Abort()
			return
		}
		key, secretEnc, err := a.Store.UpstreamKeySecret(c, keyID)
		if errors.Is(err, store.ErrNotFound) {
			upstreamFail(c, http.StatusUnauthorized, "接口密钥无效或已停用")
			c.Abort()
			return
		}
		if err != nil {
			upstreamFail(c, http.StatusInternalServerError, "读取接口密钥失败")
			c.Abort()
			return
		}
		if len(a.Cfg.MasterKey) == 0 {
			upstreamFail(c, http.StatusInternalServerError, "服务器未配置 MASTER_KEY_BASE64，无法校验接口密钥")
			c.Abort()
			return
		}
		secret, err := security.Decrypt(a.Cfg.MasterKey, secretEnc)
		if err != nil {
			upstreamFail(c, http.StatusInternalServerError, "解密接口密钥失败")
			c.Abort()
			return
		}
		sig, err := apisign.ParseRequest(get)
		if err != nil {
			upstreamFail(c, http.StatusUnauthorized, "请求缺少 time/random/signature："+err.Error())
			c.Abort()
			return
		}
		if err := sig.Verify(secret, upstreamTolerance, time.Now()); err != nil {
			a.Store.TouchUpstreamKey(c, key.KeyID, clientIP(c), err.Error())
			upstreamFail(c, http.StatusUnauthorized, "签名校验失败："+err.Error())
			c.Abort()
			return
		}
		// 防重放：signature 是 time/random/token 的摘要，正常请求的 random
		// 不会重复；同一签名在容差窗口内再次出现即可判定为重放。Redis 不可用时
		// 降级放行（记录日志），不因此阻断上游调用。
		if a.Redis != nil {
			nonce := "upstream:nonce:" + key.KeyID + ":" + security.SHA256Hex(sig.Signature)
			fresh, rerr := a.Redis.SetNX(c, nonce, 1, upstreamTolerance).Result()
			if rerr != nil {
				slog.Warn("upstream replay guard unavailable", "error", rerr)
			} else if !fresh {
				a.Store.TouchUpstreamKey(c, key.KeyID, clientIP(c), "replay rejected")
				upstreamFail(c, http.StatusUnauthorized, "请求签名已被使用（疑似重放）")
				c.Abort()
				return
			}
		} else {
			slog.Warn("upstream replay guard disabled: redis not configured")
		}
		a.Store.TouchUpstreamKey(c, key.KeyID, clientIP(c), "")
		c.Set(upstreamKeyCtx, key)
		c.Set(upstreamSecretCtx, secret)
		c.Next()
	}
}

func upstreamKeyOf(c *gin.Context) store.UpstreamKey {
	if v, ok := c.Get(upstreamKeyCtx); ok {
		if k, ok := v.(store.UpstreamKey); ok {
			return k
		}
	}
	return store.UpstreamKey{}
}

// hasUpstreamScope 判断密钥是否具备某个权限；密钥默认带上魔方对接所需的全部权限。
func hasUpstreamScope(key store.UpstreamKey, scope string) bool {
	if len(key.Scopes) == 0 {
		return true
	}
	for _, s := range key.Scopes {
		if s == scope {
			return true
		}
	}
	return false
}

// upstreamAction 读取要执行的动作，兼容魔方模块命名（host_create / host_locked / ...）
// 与 ShitIDC 的短别名。
func upstreamAction(c *gin.Context) string {
	for _, k := range []string{"act", "action"} {
		if v := strings.TrimSpace(c.Query(k)); v != "" {
			return strings.ToLower(v)
		}
		if v := strings.TrimSpace(c.PostForm(k)); v != "" {
			return strings.ToLower(v)
		}
	}
	return ""
}

// ---- 连接测试与商品列表 ----

// upstreamBusinessTest 对应魔方“接口设置”里的测试连接：签名通过即链路可用，
// 另外带回商品数量，方便在魔方后台直接核对两边目录是否一致。
func (a *App) upstreamBusinessTest(c *gin.Context) {
	key := upstreamKeyOf(c)
	products, err := a.Store.ListProducts(c)
	if err != nil {
		upstreamFail(c, http.StatusInternalServerError, "读取商品失败："+err.Error())
		return
	}
	upstreamOK(c, "success", gin.H{
		"status":        "normal",
		"server_status": 1,
		"key_id":        key.KeyID,
		"key_name":      key.Name,
		"product_num":   len(products),
		"time":          time.Now().Unix(),
	})
}

// upstreamProductRow 是魔方模块（以及商品同步）消费的商品结构。
type upstreamProductRow struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Description  string   `json:"description"`
	PriceCents   int64    `json:"price_cents"`
	Price        string   `json:"price"`
	Currency     string   `json:"currency"`
	BillingCycle string   `json:"billing_cycle"`
	Cycles       []string `json:"cycles"`
	Group        string   `json:"group"`
	Stock        int      `json:"stock"`
}

// upstreamProductList 返回全部在售商品：魔方模块用它渲染商品下拉，
// 也可以用来做目录同步。描述完全来自管理员填写的商品描述，不做任何拼装。
func (a *App) upstreamProductList(c *gin.Context) {
	if !hasUpstreamScope(upstreamKeyOf(c), "product.read") {
		upstreamFail(c, http.StatusForbidden, "该接口密钥没有 product.read 权限")
		return
	}
	products, err := a.Store.ListProducts(c)
	if err != nil {
		upstreamFail(c, http.StatusInternalServerError, "读取商品失败："+err.Error())
		return
	}
	rows := make([]upstreamProductRow, 0, len(products))
	for _, prod := range products {
		cycles := []string{prod.BillingCycle}
		if prices, perr := a.Store.ListProductPrices(c, prod.PublicID); perr == nil && len(prices) > 0 {
			cycles = cycles[:0]
			for _, pr := range prices {
				cycles = append(cycles, pr.BillingCycle)
			}
		}
		rows = append(rows, upstreamProductRow{
			ID:           prod.PublicID,
			Name:         prod.Name,
			Description:  prod.Description,
			PriceCents:   prod.PriceCents,
			Price:        formatCents(prod.PriceCents),
			Currency:     prod.Currency,
			BillingCycle: prod.BillingCycle,
			Cycles:       cycles,
			Group:        prod.GroupName,
			Stock:        -1, // -1 = 不限库存
		})
	}
	upstreamOK(c, "success", gin.H{"list": rows, "total": len(rows)})
}

func formatCents(cents int64) string {
	sign := ""
	if cents < 0 {
		sign = "-"
		cents = -cents
	}
	return fmt.Sprintf("%s%d.%02d", sign, cents/100, cents%100)
}

// ---- 主机生命周期（host_*） ----
//
// 接口：POST /compat/magiccube/v1/host?act=<动作>
// 动作沿用魔方模块命名：test / create / status / sync / locked / unlocked /
// recycle / renew，同时接受 ShitIDC 的短别名（suspend / unsuspend / terminate）。
//
// 开通与续费都从接口密钥所属用户的余额扣费：魔方作为下游不可能凭空拿到
// 上游额度，钱必须在 ShitIDC 侧真实存在，否则返回余额不足让魔方把订单挂起。

func (a *App) upstreamHostDispatch(c *gin.Context) {
	act := upstreamAction(c)
	if act == "" {
		upstreamFail(c, http.StatusBadRequest, "缺少 act 参数")
		return
	}
	switch act {
	case "test":
		a.upstreamHostTest(c)
	case "product", "products", "product_list":
		a.upstreamProductList(c)
	case "create", "host_create", "create_account":
		a.upstreamHostCreate(c)
	case "status", "host_status", "info":
		a.upstreamHostStatus(c)
	case "sync", "host_sync":
		a.upstreamHostSync(c)
	case "locked", "lock", "host_locked", "suspend":
		a.upstreamHostTransition(c, "suspend")
	case "unlocked", "unlock", "host_start", "unsuspend":
		a.upstreamHostTransition(c, "unsuspend")
	case "recycle", "delete", "host_recycle", "terminate":
		a.upstreamHostTransition(c, "terminate")
	case "renew", "host_renew", "renewal":
		a.upstreamHostRenew(c)
	default:
		upstreamFail(c, http.StatusBadRequest, "不支持的动作："+act)
	}
}

// upstreamHostTest 处理魔方“接口设置”的测试连接。魔方既可能在添加服务器时
// 调用（此时还没有主机 id），也可能对某个已有主机做连通性检查。
func (a *App) upstreamHostTest(c *gin.Context) {
	id := strings.TrimSpace(c.PostForm("id"))
	if id == "" {
		id = strings.TrimSpace(c.Query("id"))
	}
	data := gin.H{"status": "normal", "server_status": 1, "time": time.Now().Unix()}
	if id != "" {
		inst, err := a.Store.GetServiceInstance(c, id)
		if errors.Is(err, store.ErrNotFound) {
			upstreamFail(c, http.StatusNotFound, "主机不存在："+id)
			return
		}
		if err != nil {
			upstreamFail(c, http.StatusInternalServerError, "读取主机失败："+err.Error())
			return
		}
		data["id"] = inst.Service.PublicID
		data["domainstatus"] = inst.Service.Status
		data["username"] = upstreamString(inst.Data, "username")
	}
	upstreamOK(c, "success", data)
}

// upstreamString 从实例载荷里取一个字符串字段。
func upstreamString(data map[string]any, key string) string {
	if v, ok := data[key]; ok && v != nil {
		return strings.TrimSpace(fmt.Sprint(v))
	}
	return ""
}

// upstreamPost reads a form/query value.
func upstreamPost(c *gin.Context, key string) string {
	if v := strings.TrimSpace(c.PostForm(key)); v != "" {
		return v
	}
	return strings.TrimSpace(c.Query(key))
}

// resolveUpstreamHost maps the id supplied by 魔方 to a ShitIDC service. The
// module stores the service public id, so this is usually a direct hit; a
// provider_ref match is accepted as a fallback for hand-edited data.
func (a *App) resolveUpstreamHost(c *gin.Context, id string) (store.ServiceInstance, error) {
	inst, err := a.Store.GetServiceInstance(c, id)
	if err == nil {
		return inst, nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return store.ServiceInstance{}, err
	}
	return store.ServiceInstance{}, store.ErrNotFound
}

// upstreamHostCreate 开通实例。魔方模块把它叫 host_build/host_create。
// 流程与站内下单完全一致：下单（服务端重算价格）→ 余额支付 → 入队开通，
// 因此账目、审计、事件总线都天然对齐；Idempotency-Key 防重复开通。
func (a *App) upstreamHostCreate(c *gin.Context) {
	key := upstreamKeyOf(c)
	if !hasUpstreamScope(key, "order.write") {
		upstreamFail(c, http.StatusForbidden, "该接口密钥没有 order.write 权限")
		return
	}
	productRef := firstNonEmptyAPI(upstreamPost(c, "product_id"), upstreamPost(c, "product_ref"), upstreamPost(c, "pid"))
	if productRef == "" {
		upstreamFail(c, http.StatusBadRequest, "缺少 product_id：请在魔方商品配置里选择 ShitIDC 的商品")
		return
	}
	cycle := firstNonEmptyAPI(upstreamPost(c, "billing_cycle"), upstreamPost(c, "cycle"))
	if cycle == "" {
		cycle = "monthly"
	}
	quantity := 1
	if raw := upstreamPost(c, "quantity"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			quantity = n
		}
	}
	// 幂等：魔方重试开通时用同一个 hostid/请求指纹，避免重复扣费与重复开通。
	idempotency := firstNonEmptyAPI(c.GetHeader("Idempotency-Key"), upstreamPost(c, "request_id"), upstreamPost(c, "idempotency_key"))
	if idempotency == "" {
		idempotency = "magiccube:" + key.KeyID + ":" + firstNonEmptyAPI(upstreamPost(c, "hostid"), upstreamPost(c, "domain"), productRef)
	}
	if existing, err := a.Store.FindPaidServiceByPaymentKey(c, idempotency); err == nil && len(existing) > 0 {
		inst, ierr := a.Store.GetServiceInstance(c, existing[0])
		if ierr == nil {
			upstreamOK(c, "success (idempotent replay)", a.upstreamHostData(inst, ""))
			return
		}
	}
	userID := key.UserUID
	if userID == 0 {
		upstreamFail(c, http.StatusUnauthorized, "接口密钥没有归属用户")
		return
	}
	order, err := a.Store.CreateOrder(c, userID, productRef, cycle, quantity, "")
	if errors.Is(err, store.ErrNotFound) {
		upstreamFail(c, http.StatusNotFound, "商品不存在或不可订购："+productRef)
		return
	}
	if err != nil {
		upstreamFail(c, http.StatusBadRequest, "创建订单失败："+err.Error())
		return
	}
	result, err := a.Store.PayOrderWithWallet(c, userID, order.PublicID, idempotency)
	if errors.Is(err, store.ErrInsufficientBalance) {
		upstreamFail(c, http.StatusPaymentRequired, "上游余额不足，请先在 ShitIDC 充值（订单 "+order.PublicID+" 已创建，可稍后支付）")
		return
	}
	if err != nil {
		upstreamFail(c, http.StatusBadRequest, "余额支付失败："+err.Error())
		return
	}
	if len(result.ServiceIDs) == 0 {
		upstreamFail(c, http.StatusInternalServerError, "订单已支付但没有生成服务")
		return
	}
	serviceID := result.ServiceIDs[0]
	// 生成/沿用主机凭据：魔方把 $params['password'] 交给我们，留空则随机生成。
	password := upstreamPost(c, "password")
	if password == "" {
		password, _ = security.RandomToken(12)
	}
	username := firstNonEmptyAPI(upstreamPost(c, "username"), upstreamPost(c, "domain"))
	if username == "" {
		flat := strings.ReplaceAll(serviceID, "-", "")
		if len(flat) > 10 {
			flat = flat[:10]
		}
		username = "u" + flat
	}
	payload := map[string]any{"username": username, "password": password, "provider": "magiccube", "upstream_key": key.KeyID}
	if err := a.Store.MarkService(c, serviceID, "pending", "", payload); err != nil {
		upstreamFail(c, http.StatusInternalServerError, "写入实例信息失败："+err.Error())
		return
	}
	// 交给 worker 真正开通（可能调用上游 Provider），这一步与站内下单同一条链路。
	if a.Queue != nil {
		_ = a.Queue.Provision(serviceID)
	}
	a.Bus.Emit(a.eventCtx(c), events.OrderPaid, map[string]any{"order_id": order.PublicID, "user_id": key.UserUID, "method": "wallet", "kind": "magiccube", "service_id": serviceID})
	_ = a.Store.RecordUpstreamHostLink(c, key.PublicID, serviceID, upstreamPost(c, "hostid"), upstreamPost(c, "user_id"), upstreamPost(c, "domain"))
	_ = a.Store.Audit(c, userID, "magiccube.host.create", "service", serviceID, c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, map[string]any{"order_id": order.PublicID, "key_id": key.KeyID, "product": productRef})
	inst, err := a.Store.GetServiceInstance(c, serviceID)
	if err != nil {
		upstreamFail(c, http.StatusInternalServerError, "开通已提交但读取服务失败："+err.Error())
		return
	}
	upstreamOK(c, "success", a.upstreamHostData(inst, password))
}

// upstreamHostData builds the payload 魔方 shows as 主机信息 / 用户名密码。
func (a *App) upstreamHostData(inst store.ServiceInstance, plainPassword string) gin.H {
	username := upstreamString(inst.Data, "username")
	password := plainPassword
	if password == "" {
		password = upstreamString(inst.Data, "password")
	}
	return gin.H{
		"id":            inst.Service.PublicID,
		"hostid":        inst.Service.PublicID,
		"domainstatus":  inst.Service.Status,
		"status":        upstreamStatusWord(inst.Service.Status),
		"des":           upstreamStatusText(inst.Service.Status),
		"username":      username,
		"password":      password,
		"product_name":  inst.Service.ProductName,
		"billing_cycle": inst.Service.BillingCycle,
		"nextduedate":   upstreamDueDate(inst.Service.ExpiresAt),
		"regdate":       inst.Service.CreatedAt.Format("2006-01-02"),
	}
}

// upstreamStatusWord maps a ShitIDC service status to the 魔方 status word.
func upstreamStatusWord(status string) string {
	switch status {
	case "active":
		return "on"
	case "suspended":
		return "off"
	case "pending", "provisioning", "suspending", "unsuspending":
		return "waiting"
	case "terminated", "terminating", "failed":
		return "off"
	default:
		return "unknown"
	}
}

func upstreamStatusText(status string) string {
	switch status {
	case "active":
		return "运行中"
	case "suspended":
		return "已暂停"
	case "pending", "provisioning":
		return "开通中"
	case "suspending", "unsuspending":
		return "处理中"
	case "terminated", "terminating":
		return "已删除"
	default:
		return "未知"
	}
}

func upstreamDueDate(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.Format("2006-01-02")
}

// upstreamHostStatus 查询单个主机状态，对应模块的 host_status / Status。
func (a *App) upstreamHostStatus(c *gin.Context) {
	id := firstNonEmptyAPI(upstreamPost(c, "id"), upstreamPost(c, "hostid"), upstreamPost(c, "service_id"))
	if id == "" {
		upstreamFail(c, http.StatusBadRequest, "缺少 id")
		return
	}
	inst, err := a.resolveUpstreamHost(c, id)
	if errors.Is(err, store.ErrNotFound) {
		upstreamFail(c, http.StatusNotFound, "主机不存在："+id)
		return
	}
	if err != nil {
		upstreamFail(c, http.StatusInternalServerError, "读取主机失败："+err.Error())
		return
	}
	upstreamOK(c, "success", a.upstreamHostData(inst, ""))
}

// upstreamHostSync 让魔方把本地记录与上游对齐（用户名、密码、到期日）。
func (a *App) upstreamHostSync(c *gin.Context) {
	id := firstNonEmptyAPI(upstreamPost(c, "id"), upstreamPost(c, "hostid"), upstreamPost(c, "service_id"))
	inst, err := a.resolveUpstreamHost(c, id)
	if errors.Is(err, store.ErrNotFound) {
		upstreamFail(c, http.StatusNotFound, "主机不存在："+id)
		return
	}
	if err != nil {
		upstreamFail(c, http.StatusInternalServerError, "读取主机失败："+err.Error())
		return
	}
	_ = a.Store.RecordUpstreamHostLink(c, upstreamKeyOf(c).PublicID, inst.Service.PublicID, id, upstreamPost(c, "user_id"), upstreamPost(c, "domain"))
	upstreamOK(c, "success", a.upstreamHostData(inst, ""))
}

// upstreamHostTransitionHandler adapts upstreamHostTransition to a gin handler
// for the dedicated /host/locked, /host/start and /host/recycle endpoints.
func (a *App) upstreamHostTransitionHandler(action string) gin.HandlerFunc {
	return func(c *gin.Context) { a.upstreamHostTransition(c, action) }
}

// upstreamHostTransition 处理暂停/解除暂停/删除。它与管理后台的按钮走同一套
// 抢占式状态机（ClaimServiceForTransition），因此并发操作只会有一个生效，
// 失败时状态自动回滚并写入 last_transition_error。
func (a *App) upstreamHostTransition(c *gin.Context, action string) {
	key := upstreamKeyOf(c)
	if !hasUpstreamScope(key, "service.operate") {
		upstreamFail(c, http.StatusForbidden, "该接口密钥没有 service.operate 权限")
		return
	}
	id := firstNonEmptyAPI(upstreamPost(c, "id"), upstreamPost(c, "hostid"), upstreamPost(c, "service_id"))
	if id == "" {
		upstreamFail(c, http.StatusBadRequest, "缺少 id")
		return
	}
	owner, err := a.Store.GetServiceOwner(c, id)
	if errors.Is(err, store.ErrNotFound) {
		upstreamFail(c, http.StatusNotFound, "主机不存在："+id)
		return
	}
	if err != nil {
		upstreamFail(c, http.StatusInternalServerError, "读取主机失败："+err.Error())
		return
	}
	// 一个接口密钥只能操作它自己开通的服务，避免越权管理别人名下的实例。
	if owner != key.UserUID {
		upstreamFail(c, http.StatusForbidden, "该主机不属于此接口密钥的账号")
		return
	}
	var claimed bool
	switch action {
	case "suspend":
		_, _, claimed, err = a.Store.ClaimServiceForTransition(c, id, "suspending", "active")
	case "unsuspend":
		_, _, claimed, err = a.Store.ClaimServiceForTransition(c, id, "unsuspending", "suspended")
	case "terminate":
		_, _, claimed, err = a.Store.ClaimServiceForTransition(c, id, "terminating", "active", "suspended")
	default:
		upstreamFail(c, http.StatusBadRequest, "不支持的操作："+action)
		return
	}
	if err != nil {
		upstreamFail(c, http.StatusInternalServerError, "状态流转失败："+err.Error())
		return
	}
	if !claimed {
		upstreamFail(c, http.StatusConflict, "主机当前状态不允许该操作")
		return
	}
	if a.Queue == nil {
		_ = a.Store.FinalizeServiceTransition(c, id, "", false, "task queue unavailable")
		upstreamFail(c, http.StatusServiceUnavailable, "任务队列不可用，操作未提交")
		return
	}
	switch action {
	case "suspend":
		err = a.Queue.ServiceSuspend(id)
	case "unsuspend":
		err = a.Queue.ServiceUnsuspend(id)
	case "terminate":
		err = a.Queue.ServiceTerminate(id)
	}
	if err != nil {
		_ = a.Store.FinalizeServiceTransition(c, id, "", false, err.Error())
		upstreamFail(c, http.StatusServiceUnavailable, "任务入队失败："+err.Error())
		return
	}
	_ = a.Store.Audit(c, key.UserUID, "magiccube.host."+action, "service", id, c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, map[string]any{"key_id": key.KeyID})
	upstreamOK(c, "success", gin.H{"id": id, "action": action})
}

// upstreamHostRenew 续费：生成续费订单并立即用余额结算，然后入队调用上游。
// 与站内续费同一条链路，因此到期时间顺延、不重复开通实例。
func (a *App) upstreamHostRenew(c *gin.Context) {
	key := upstreamKeyOf(c)
	if !hasUpstreamScope(key, "order.write") {
		upstreamFail(c, http.StatusForbidden, "该接口密钥没有 order.write 权限")
		return
	}
	id := firstNonEmptyAPI(upstreamPost(c, "id"), upstreamPost(c, "hostid"), upstreamPost(c, "service_id"))
	if id == "" {
		upstreamFail(c, http.StatusBadRequest, "缺少 id")
		return
	}
	owner, err := a.Store.GetServiceOwner(c, id)
	if errors.Is(err, store.ErrNotFound) {
		upstreamFail(c, http.StatusNotFound, "主机不存在："+id)
		return
	}
	if err != nil {
		upstreamFail(c, http.StatusInternalServerError, "读取主机失败："+err.Error())
		return
	}
	if owner != key.UserUID {
		upstreamFail(c, http.StatusForbidden, "该主机不属于此接口密钥的账号")
		return
	}
	order, err := a.Store.CreateRenewalOrder(c, key.UserUID, id)
	if errors.Is(err, store.ErrNotFound) {
		upstreamFail(c, http.StatusNotFound, "主机不存在："+id)
		return
	}
	if errors.Is(err, store.ErrInvalidState) {
		upstreamFail(c, http.StatusConflict, "主机当前状态不可续费")
		return
	}
	if err != nil {
		upstreamFail(c, http.StatusBadRequest, "生成续费订单失败："+err.Error())
		return
	}
	if _, err := a.Store.PayOrderWithWallet(c, key.UserUID, order.PublicID, ""); err != nil {
		if errors.Is(err, store.ErrInsufficientBalance) {
			upstreamFail(c, http.StatusPaymentRequired, "上游余额不足，续费订单 "+order.PublicID+" 已创建，请充值后重试")
			return
		}
		upstreamFail(c, http.StatusBadRequest, "续费支付失败："+err.Error())
		return
	}
	_ = a.Store.Audit(c, key.UserUID, "magiccube.host.renew", "service", id, c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, map[string]any{"order_id": order.PublicID, "key_id": key.KeyID})
	inst, err := a.Store.GetServiceInstance(c, id)
	if err != nil {
		upstreamOK(c, "续费成功", gin.H{"id": id, "nextduedate": ""})
		return
	}
	upstreamOK(c, "success", a.upstreamHostData(inst, ""))
}

// ---- 管理端：魔方上游访问密钥 ----
//
// 管理员在这里生成一把密钥，然后把 key_id 和 secret 一起填到魔方后台
// “接口设置 -> 添加接口”的 Hash(accesshash) 字段：魔方模块用 secret 参与
// 签名，用 key_id 作为 token 值回传，我们据此定位密钥并验签。

func (a *App) adminListUpstreamKeys(c *gin.Context) {
	keys, err := a.Store.ListUpstreamKeys(c)
	if err != nil {
		httpx.Fail(c, 500, "UPSTREAM_KEYS_FAILED", "读取魔方接口密钥失败")
		return
	}
	// 附带接入说明，前端直接展示，省得管理员去翻文档。
	httpx.OK(c, 200, map[string]any{
		"keys":      keys,
		"base_url":  strings.TrimRight(a.Cfg.PublicBaseURL, "/"),
		"test_path": "/compat/magiccube/v1/test",
		"host_path": "/compat/magiccube/v1/host",
		"prod_path": "/compat/magiccube/v1/product",
	})
}

func (a *App) adminCreateUpstreamKey(c *gin.Context) {
	p, _ := getPrincipal(c)
	var in struct {
		Name   string   `json:"name"`
		UserID string   `json:"user_id"` // 归属用户（公开 ID 或 UID），缺省=当前管理员
		Scopes []string `json:"scopes"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	// 默认归属当前管理员，也可以按 UID 指定一个用户作为“上游客户”：
	// 魔方通过这把密钥开通的服务都记在该用户名下，并从其余额扣费。
	ownerID := p.User.ID
	if raw := strings.TrimSpace(in.UserID); raw != "" {
		// GetUserByIDOrPublicID accepts the sequential UID ("1") or the UUID.
		owner, err := a.Store.GetUserByIDOrPublicID(c, raw)
		if err != nil {
			httpx.Fail(c, 400, "USER_NOT_FOUND", "归属用户不存在："+raw)
			return
		}
		ownerID = owner.ID
	}
	if len(a.Cfg.MasterKey) == 0 {
		httpx.Fail(c, 500, "MASTER_KEY_MISSING", "服务器未配置 MASTER_KEY_BASE64，无法加密保存接口密钥")
		return
	}
	keyID := "zj_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:16]
	secret, err := security.RandomToken(32)
	if err != nil {
		httpx.Fail(c, 500, "INTERNAL_ERROR", "生成密钥失败")
		return
	}
	secretEnc, err := security.Encrypt(a.Cfg.MasterKey, secret)
	if err != nil {
		httpx.Fail(c, 500, "MASTER_KEY_INVALID", "加密接口密钥失败")
		return
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		name = "魔方财务接口"
	}
	key, err := a.Store.CreateUpstreamKey(c, ownerID, name, keyID, secretEnc, in.Scopes)
	if err != nil {
		httpx.Fail(c, 400, "UPSTREAM_KEY_CREATE_FAILED", err.Error())
		return
	}
	_ = a.Store.Audit(c, p.User.ID, "upstream_key.create", "upstream_key", key.PublicID, c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, map[string]any{"key_id": key.KeyID, "owner_user_id": ownerID})
	accessHash := key.KeyID + "." + secret
	httpx.OK(c, 201, map[string]any{
		"key":         key,
		"access_hash": accessHash,
		"key_id":      key.KeyID,
		"secret":      secret,
		"warning":     "Secret 仅在本次返回，请立刻填到魔方后台，之后无法再次查看",
	})
}

func (a *App) adminUpdateUpstreamKey(c *gin.Context) {
	p, _ := getPrincipal(c)
	var in struct {
		Active *bool `json:"active"`
	}
	if err := c.ShouldBindJSON(&in); err != nil || in.Active == nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	if err := a.Store.SetUpstreamKeyActive(c, c.Param("id"), *in.Active); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpx.Fail(c, 404, "UPSTREAM_KEY_NOT_FOUND", "接口密钥不存在")
			return
		}
		httpx.Fail(c, 400, "UPSTREAM_KEY_UPDATE_FAILED", err.Error())
		return
	}
	_ = a.Store.Audit(c, p.User.ID, "upstream_key.update", "upstream_key", c.Param("id"), c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, in)
	httpx.OK(c, 200, map[string]bool{"ok": true})
}

func (a *App) adminDeleteUpstreamKey(c *gin.Context) {
	p, _ := getPrincipal(c)
	if err := a.Store.DeleteUpstreamKey(c, c.Param("id")); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpx.Fail(c, 404, "UPSTREAM_KEY_NOT_FOUND", "接口密钥不存在")
			return
		}
		httpx.Fail(c, 400, "UPSTREAM_KEY_DELETE_FAILED", err.Error())
		return
	}
	_ = a.Store.Audit(c, p.User.ID, "upstream_key.delete", "upstream_key", c.Param("id"), c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, nil)
	httpx.OK(c, 200, map[string]bool{"ok": true})
}

// adminUpstreamKeyLinks 列出某把密钥开通出来的主机，便于对账。
func (a *App) adminUpstreamKeyLinks(c *gin.Context) {
	links, err := a.Store.ListUpstreamHostLinks(c, c.Param("id"), parseIntDefault(c.Query("limit"), 200))
	if err != nil {
		httpx.Fail(c, 500, "UPSTREAM_LINKS_FAILED", "读取对接主机失败")
		return
	}
	httpx.OK(c, 200, links)
}
