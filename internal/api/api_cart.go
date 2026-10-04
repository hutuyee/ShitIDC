package api

import (
	"errors"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/hutuyee/ShitIDC/internal/httpx"
	"github.com/hutuyee/ShitIDC/internal/security"
	"github.com/hutuyee/ShitIDC/internal/store"
)

// 购物车多商品结算（对应魔方 shd_cart_session）。

// getCart 返回购物车，价格每次都用当前定价重算。
func (a *App) getCart(c *gin.Context) {
	pr, _ := getPrincipal(c)
	cart, err := a.Store.GetCart(c, pr.User.ID)
	if err != nil {
		httpx.Fail(c, 500, "CART_READ_FAILED", "读取购物车失败")
		return
	}
	httpx.OK(c, 200, cart)
}

// addCartItem 把商品加入购物车。
func (a *App) addCartItem(c *gin.Context) {
	pr, _ := getPrincipal(c)
	var in struct {
		ProductID    string               `json:"product_id"`
		BillingCycle string               `json:"billing_cycle"`
		Quantity     int                  `json:"quantity"`
		Config       []store.ConfigChoice `json:"config"`
		CustomFields map[string]string    `json:"custom_fields"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	if strings.TrimSpace(in.ProductID) == "" {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请选择商品")
		return
	}
	if in.Quantity <= 0 {
		in.Quantity = 1
	}
	if in.BillingCycle == "" {
		in.BillingCycle = "monthly"
	}
	if _, err := a.Store.AddCartItem(c, pr.User.ID, in.ProductID, in.BillingCycle, in.Quantity, store.OrderConfigInput{
		Choices: in.Config, CustomFields: in.CustomFields,
	}); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpx.Fail(c, 404, "PRODUCT_NOT_FOUND", "商品不存在或已下架")
			return
		}
		if errors.Is(err, store.ErrCartCurrencyMismatch) {
			httpx.Fail(c, 409, "CART_CURRENCY_MISMATCH", err.Error())
			return
		}
		httpx.Fail(c, 400, "CART_ADD_FAILED", err.Error())
		return
	}
	// 回传整车，前端直接刷新即可。
	cart, err := a.Store.GetCart(c, pr.User.ID)
	if err != nil {
		httpx.Fail(c, 500, "CART_READ_FAILED", "读取购物车失败")
		return
	}
	httpx.OK(c, 201, cart)
}

// removeCartItem 从购物车移除一项。
func (a *App) removeCartItem(c *gin.Context) {
	pr, _ := getPrincipal(c)
	if err := a.Store.RemoveCartItem(c, pr.User.ID, c.Param("id")); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpx.Fail(c, 404, "CART_ITEM_NOT_FOUND", "购物车里没有这一项")
			return
		}
		httpx.Fail(c, 500, "CART_REMOVE_FAILED", "移除失败")
		return
	}
	cart, err := a.Store.GetCart(c, pr.User.ID)
	if err != nil {
		httpx.Fail(c, 500, "CART_READ_FAILED", "读取购物车失败")
		return
	}
	httpx.OK(c, 200, cart)
}

// clearCart 清空购物车。
func (a *App) clearCart(c *gin.Context) {
	pr, _ := getPrincipal(c)
	n, err := a.Store.ClearCart(c, pr.User.ID)
	if err != nil {
		httpx.Fail(c, 500, "CART_CLEAR_FAILED", "清空失败")
		return
	}
	httpx.OK(c, 200, map[string]any{"removed": n})
}

// checkoutCart 把购物车整批结算成订单。
//
// 这里**不收钱**：结算只生成一批待付款订单，付款走 payCheckout 或逐单支付。
func (a *App) checkoutCart(c *gin.Context) {
	pr, _ := getPrincipal(c)
	var in struct {
		CouponCode string `json:"coupon_code"`
	}
	_ = c.ShouldBindJSON(&in)
	// 结算前先看整车是否可售：不可售就让用户先清理，而不是建出一批注定失败的订单。
	cart, err := a.Store.GetCart(c, pr.User.ID)
	if err != nil {
		httpx.Fail(c, 500, "CART_READ_FAILED", "读取购物车失败")
		return
	}
	if cart.Count == 0 {
		httpx.Fail(c, 400, "CART_EMPTY", "购物车是空的")
		return
	}
	if !cart.Payable {
		httpx.Fail(c, 409, "CART_NOT_PAYABLE", "购物车里有不可售的商品，请先移除")
		return
	}
	res, err := a.Store.CheckoutCart(c, pr.User.ID, strings.TrimSpace(in.CouponCode))
	if err != nil {
		httpx.Fail(c, 400, "CART_CHECKOUT_FAILED", err.Error())
		return
	}
	_ = a.Store.Audit(c, pr.User.ID, "cart.checkout", "checkout_group", res.GroupID, c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, map[string]any{"orders": len(res.OrderIDs), "total": res.TotalCents})
	httpx.OK(c, 201, res)
}

// payCheckout 用余额一次性支付整批订单。
func (a *App) payCheckout(c *gin.Context) {
	pr, _ := getPrincipal(c)
	// 幂等键：客户端重试不会重复扣款。没给就现生成一个。
	idempotency := strings.TrimSpace(c.GetHeader("Idempotency-Key"))
	if idempotency == "" {
		idempotency, _ = security.RandomToken(16)
	}
	res, err := a.Store.PayCheckoutGroup(c, pr.User.ID, c.Param("id"), idempotency)
	if err != nil {
		switch {
		case errors.Is(err, store.ErrNotFound):
			httpx.Fail(c, 404, "CHECKOUT_NOT_FOUND", "结算批次不存在")
		case errors.Is(err, store.ErrInsufficientBalance):
			httpx.Fail(c, 402, "INSUFFICIENT_BALANCE", "余额不足，请先充值")
		case errors.Is(err, store.ErrInvalidState):
			httpx.Fail(c, 409, "CHECKOUT_NOT_PAYABLE", "该批次当前不可支付")
		default:
			httpx.Fail(c, 400, "CHECKOUT_PAY_FAILED", err.Error())
		}
		return
	}
	_ = a.Store.Audit(c, pr.User.ID, "cart.pay", "checkout_group", res.GroupID, c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, map[string]any{"total": res.TotalCents})
	httpx.OK(c, 200, res)
}
