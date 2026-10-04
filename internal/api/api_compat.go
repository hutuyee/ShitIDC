package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/hutuyee/ShitIDC/internal/events"
	"github.com/hutuyee/ShitIDC/internal/httpx"
	"github.com/hutuyee/ShitIDC/internal/provider"
	"github.com/hutuyee/ShitIDC/internal/security"
	"github.com/hutuyee/ShitIDC/internal/store"
)

// compatFail answers with the magiccube-style error envelope ({status,msg})
// so downstream adapters surface the message instead of choking on the
// standard data/error envelope.
func compatFail(c *gin.Context, status int, msg string) {
	c.JSON(status, map[string]any{"status": status, "msg": msg})
}

// renewService creates an unpaid renewal order for one of the caller's
// services; paying it (wallet or online) extends the service's expiry.
func (a *App) renewService(c *gin.Context) {
	p, _ := getPrincipal(c)
	o, err := a.Store.CreateRenewalOrder(c, p.User.ID, c.Param("id"))
	switch {
	case errors.Is(err, store.ErrNotFound):
		httpx.Fail(c, 404, "SERVICE_NOT_FOUND", "服务不存在")
		return
	case errors.Is(err, store.ErrInvalidState):
		httpx.Fail(c, 409, "SERVICE_NOT_RENEWABLE", "仅生效中或已暂停的服务可以续费")
		return
	case err != nil:
		httpx.Fail(c, 400, "RENEW_ORDER_FAILED", err.Error())
		return
	}
	_ = a.Store.Audit(c, p.User.ID, "service.renew_order", "service", c.Param("id"), c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, map[string]any{"order_id": o.PublicID, "amount_cents": o.TotalCents})
	httpx.OK(c, 201, o)
}

// compatMagicCubeLogin lets a downstream 魔方财务 (or any reseller panel)
// use ShitIDC as its upstream. The downstream panel posts account+password
// to /compat/magiccube/v1/login_api; the "password" is a ShitIDC API token
// in its full keyID.secret form, and the returned jwt is that same token so
// subsequent requests carry `Authorization: Bearer keyID.secret` — which the
// standard API-token authentication already understands. No new session
// system, no second credential format to keep secure.
func (a *App) compatMagicCubeLogin(c *gin.Context) {
	values := map[string]string{}
	// Downstream panels typically post x-www-form-urlencoded; accept JSON too.
	if ct := c.ContentType(); strings.HasPrefix(ct, "application/json") {
		var in struct {
			Account  string `json:"account"`
			Password string `json:"password"`
			APIKey   string `json:"api_key"`
		}
		if err := c.ShouldBindJSON(&in); err == nil {
			values["account"] = in.Account
			values["password"] = firstNonEmptyAPI(in.Password, in.APIKey)
		}
	} else {
		_ = c.Request.ParseForm()
		values["account"] = c.Request.FormValue("account")
		values["password"] = firstNonEmptyAPI(c.Request.FormValue("password"), c.Request.FormValue("api_key"))
	}
	token := strings.TrimSpace(values["password"])
	parts := strings.SplitN(token, ".", 2)
	if len(parts) != 2 || len(a.Cfg.MasterKey) == 0 {
		c.JSON(http.StatusUnauthorized, map[string]any{"status": 401, "msg": "invalid api key"})
		return
	}
	hash, err := security.HMACSecret(a.Cfg.MasterKey, parts[1])
	if err != nil {
		c.JSON(http.StatusUnauthorized, map[string]any{"status": 401, "msg": "invalid api key"})
		return
	}
	u, scopes, err := a.Store.AuthenticateAPIToken(c, parts[0], hash, clientIP(c))
	if err != nil {
		c.JSON(http.StatusUnauthorized, map[string]any{"status": 401, "msg": "invalid api key"})
		return
	}
	allowed := false
	for _, s := range scopes {
		if s == "product.read" {
			allowed = true
			break
		}
	}
	if !allowed {
		c.JSON(http.StatusUnauthorized, map[string]any{"status": 401, "msg": "invalid api key or missing product.read scope"})
		return
	}
	_ = a.Store.Audit(c, u.ID, "compat.magiccube_login", "api_token", parts[0], c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, nil)
	// Mirror the magiccube login response shape; downstream adapters look for
	// jwt / token / data.jwt depending on version, so provide all of them.
	c.JSON(http.StatusOK, map[string]any{
		"status": 200,
		"msg":    "success",
		"jwt":    token,
		"token":  token,
		"data":   map[string]any{"jwt": token, "token": token, "email": u.Email},
	})
}

// ---- resource operations (第三十阶段: 让魔方把 ShitIDC 当上游) ----
//
// These endpoints mirror the exact contract internal/provider/magiccube
// consumes when ShitIDC talks to a MagicCube upstream, so a downstream
// panel can use ShitIDC as its resource provider with the same adapter:
//
//	POST /compat/magiccube/v1/create      body = provider.CreateRequest JSON
//	POST /compat/magiccube/v1/suspend     body = {"id": "<service public id>"}
//	POST /compat/magiccube/v1/unsuspend   body = {"id": ...}
//	POST /compat/magiccube/v1/terminate   body = {"id": ...}
//	POST /compat/magiccube/v1/renew       body = {"id": ...}
//
// Billing stays inside the core: create and renew debit the API token
// owner's wallet in a serializable transaction, and the create honors the
// Idempotency-Key header so a gateway retry can never provision twice.

// compatCreateService provisions a paid service on behalf of the token owner.
func (a *App) compatCreateService(c *gin.Context) {
	p, _ := getPrincipal(c)
	var in provider.CreateRequest
	if err := c.ShouldBindJSON(&in); err != nil {
		compatFail(c, http.StatusBadRequest, "invalid create request body")
		return
	}
	if strings.TrimSpace(in.ProductRef) == "" {
		compatFail(c, http.StatusBadRequest, "product_ref is required")
		return
	}
	cycle := "monthly"
	if v, ok := in.Options["billing_cycle"].(string); ok && strings.TrimSpace(v) != "" {
		cycle = strings.TrimSpace(v)
	}
	idempotency := strings.TrimSpace(c.GetHeader("Idempotency-Key"))
	if idempotency == "" {
		idempotency = strings.TrimSpace(in.RequestID)
	}
	if idempotency != "" {
		if existing, err := a.Store.FindPaidServiceByPaymentKey(c, idempotency); err == nil && len(existing) > 0 {
			c.JSON(http.StatusOK, map[string]any{"status": 200, "msg": "success (idempotent replay)", "id": existing[0], "data": map[string]any{"id": existing[0]}})
			return
		}
	}
	o, err := a.Store.CreateOrder(c, p.User.ID, in.ProductRef, cycle, 1, "")
	if errors.Is(err, store.ErrNotFound) {
		compatFail(c, http.StatusNotFound, "product not found or not orderable")
		return
	}
	if err != nil {
		compatFail(c, http.StatusBadRequest, "create order failed: "+err.Error())
		return
	}
	result, err := a.Store.PayOrderWithWallet(c, p.User.ID, o.PublicID, idempotency)
	if errors.Is(err, store.ErrInsufficientBalance) {
		compatFail(c, http.StatusPaymentRequired, "insufficient wallet balance")
		return
	}
	if err != nil {
		compatFail(c, http.StatusBadRequest, "payment failed: "+err.Error())
		return
	}
	for _, id := range result.ServiceIDs {
		if a.Queue != nil {
			_ = a.Queue.Provision(id)
		}
		a.Bus.Emit(a.eventCtx(c), events.OrderPaid, map[string]any{"order_id": o.PublicID, "user_id": p.User.PublicID, "method": "wallet", "kind": "compat", "service_id": id})
		_ = a.Store.Audit(c, p.User.ID, "compat.service.create", "service", id, c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, map[string]any{"order_id": o.PublicID})
		c.JSON(http.StatusOK, map[string]any{"status": 200, "msg": "success", "id": id, "data": map[string]any{"id": id}})
		return
	}
	compatFail(c, http.StatusInternalServerError, "order paid but no service was created")
}

// compatServiceAction handles suspend/unsuspend/terminate for services the
// token owner provisioned through the compat layer.
func (a *App) compatServiceAction(action string) gin.HandlerFunc {
	return func(c *gin.Context) {
		p, _ := getPrincipal(c)
		var in struct {
			ID string `json:"id"`
		}
		if err := c.ShouldBindJSON(&in); err != nil || strings.TrimSpace(in.ID) == "" {
			compatFail(c, http.StatusBadRequest, "body must be {\"id\": \"<service id>\"}")
			return
		}
		owner, err := a.Store.GetServiceOwner(c, in.ID)
		if errors.Is(err, store.ErrNotFound) {
			compatFail(c, http.StatusNotFound, "service not found")
			return
		}
		if err != nil {
			compatFail(c, http.StatusInternalServerError, "resolve service failed")
			return
		}
		if owner != p.User.ID {
			compatFail(c, http.StatusForbidden, "service belongs to another account")
			return
		}
		var claimed bool
		switch action {
		case "suspend":
			_, _, claimed, err = a.Store.ClaimServiceForTransition(c, in.ID, "suspending", "active")
		case "unsuspend":
			_, _, claimed, err = a.Store.ClaimServiceForTransition(c, in.ID, "unsuspending", "suspended")
		case "terminate":
			_, _, claimed, err = a.Store.ClaimServiceForTransition(c, in.ID, "terminating", "active", "suspended")
		}
		if err != nil {
			compatFail(c, http.StatusInternalServerError, "claim transition failed")
			return
		}
		if !claimed {
			compatFail(c, http.StatusConflict, "service is not in a state that allows "+action)
			return
		}
		if a.Queue == nil {
			_ = a.Store.FinalizeServiceTransition(c, in.ID, "", false, "queue unavailable")
			compatFail(c, http.StatusServiceUnavailable, "task queue unavailable")
			return
		}
		switch action {
		case "suspend":
			_ = a.Queue.ServiceSuspend(in.ID)
		case "unsuspend":
			_ = a.Queue.ServiceUnsuspend(in.ID)
		case "terminate":
			_ = a.Queue.ServiceTerminate(in.ID)
		}
		_ = a.Store.Audit(c, p.User.ID, "compat.service."+action, "service", in.ID, c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, nil)
		c.JSON(http.StatusOK, map[string]any{"status": 200, "msg": "success"})
	}
}

// compatRenewService extends a service expiry: it creates the renewal order
// and settles it from the wallet immediately, then queues the provider
// renew call. No credit is extended — renewal requires a funded wallet.
func (a *App) compatRenewService(c *gin.Context) {
	p, _ := getPrincipal(c)
	var in struct {
		ID string `json:"id"`
	}
	if err := c.ShouldBindJSON(&in); err != nil || strings.TrimSpace(in.ID) == "" {
		compatFail(c, http.StatusBadRequest, "body must be {\"id\": \"<service id>\"}")
		return
	}
	owner, err := a.Store.GetServiceOwner(c, in.ID)
	if errors.Is(err, store.ErrNotFound) {
		compatFail(c, http.StatusNotFound, "service not found")
		return
	}
	if err != nil {
		compatFail(c, http.StatusInternalServerError, "resolve service failed")
		return
	}
	if owner != p.User.ID {
		compatFail(c, http.StatusForbidden, "service belongs to another account")
		return
	}
	o, err := a.Store.CreateRenewalOrder(c, p.User.ID, in.ID)
	if errors.Is(err, store.ErrNotFound) {
		compatFail(c, http.StatusNotFound, "service not found")
		return
	}
	if errors.Is(err, store.ErrInvalidState) {
		compatFail(c, http.StatusConflict, "service is not renewable in its current state")
		return
	}
	if err != nil {
		compatFail(c, http.StatusBadRequest, "renewal order failed: "+err.Error())
		return
	}
	result, err := a.Store.PayOrderWithWallet(c, p.User.ID, o.PublicID, "")
	if errors.Is(err, store.ErrInsufficientBalance) {
		compatFail(c, http.StatusPaymentRequired, "insufficient wallet balance for renewal")
		return
	}
	if err != nil {
		compatFail(c, http.StatusBadRequest, "payment failed: "+err.Error())
		return
	}
	if result.RenewServiceID != "" && a.Queue != nil {
		_ = a.Queue.ServiceRenew(result.RenewServiceID)
	}
	a.Bus.Emit(a.eventCtx(c), events.OrderPaid, map[string]any{"order_id": o.PublicID, "user_id": p.User.PublicID, "method": "wallet", "kind": "compat_renewal"})
	_ = a.Store.Audit(c, p.User.ID, "compat.service.renew", "service", in.ID, c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, map[string]any{"order_id": o.PublicID})
	c.JSON(http.StatusOK, map[string]any{"status": 200, "msg": "success", "id": in.ID, "expires_extended": true})
}
