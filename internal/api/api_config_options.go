package api

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/hutuyee/ShitIDC/internal/httpx"
	"github.com/hutuyee/ShitIDC/internal/model"
	"github.com/hutuyee/ShitIDC/internal/store"
)

// ---- 管理端：配置项 CRUD ----

// adminListConfigOptions 返回某商品的可配置项。
func (a *App) adminListConfigOptions(c *gin.Context) {
	items, err := a.Store.ListProductConfigOptions(c, c.Param("id"))
	if err != nil {
		httpx.Fail(c, 500, "CONFIG_OPTIONS_FAILED", "读取配置项失败")
		return
	}
	httpx.OK(c, 200, items)
}

// adminCreateConfigOption 新建一个配置项连同它的候选项。
// 请求体：{ name, description, option_type, required, sort_weight, qty_min, qty_max, values: [{label, price_cents, setup_cents, is_default, sort_weight}] }
func (a *App) adminCreateConfigOption(c *gin.Context) {
	p, _ := getPrincipal(c)
	var in adminConfigOptionInput
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	if err := in.validate(); err != nil {
		httpx.Fail(c, 400, "INVALID_CONFIG_OPTION", err.Error())
		return
	}
	opt, err := a.Store.CreateConfigOption(c, c.Param("id"), in.toStoreInput())
	if err != nil {
		httpx.Fail(c, 400, "CONFIG_OPTION_CREATE_FAILED", err.Error())
		return
	}
	_ = a.Store.Audit(c, p.User.ID, "config_option.create", "product", c.Param("id"), c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, opt)
	httpx.OK(c, 201, opt)
}

// adminUpdateConfigOption 覆盖一个配置项（含候选项的增删改）。
func (a *App) adminUpdateConfigOption(c *gin.Context) {
	p, _ := getPrincipal(c)
	var in adminConfigOptionInput
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	if err := in.validate(); err != nil {
		httpx.Fail(c, 400, "INVALID_CONFIG_OPTION", err.Error())
		return
	}
	if err := a.Store.UpdateConfigOption(c, c.Param("option_id"), in.toStoreInput()); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpx.Fail(c, 404, "CONFIG_OPTION_NOT_FOUND", "配置项不存在")
			return
		}
		httpx.Fail(c, 400, "CONFIG_OPTION_UPDATE_FAILED", err.Error())
		return
	}
	_ = a.Store.Audit(c, p.User.ID, "config_option.update", "config_option", c.Param("option_id"), c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, in)
	httpx.OK(c, 200, map[string]bool{"ok": true})
}

func (a *App) adminDeleteConfigOption(c *gin.Context) {
	p, _ := getPrincipal(c)
	if err := a.Store.DeleteConfigOption(c, c.Param("option_id")); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpx.Fail(c, 404, "CONFIG_OPTION_NOT_FOUND", "配置项不存在")
			return
		}
		httpx.Fail(c, 400, "CONFIG_OPTION_DELETE_FAILED", err.Error())
		return
	}
	_ = a.Store.Audit(c, p.User.ID, "config_option.delete", "config_option", c.Param("option_id"), c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, nil)
	httpx.OK(c, 200, map[string]bool{"ok": true})
}

// adminConfigOptionInput 是配置项的请求体。
type adminConfigOptionInput struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	OptionType  int    `json:"option_type"`
	Required    bool   `json:"required"`
	SortWeight  int    `json:"sort_weight"`
	QtyMin      int    `json:"qty_min"`
	QtyMax      int    `json:"qty_max"`
	Values      []struct {
		Label      string `json:"label"`
		PriceCents int64  `json:"price_cents"`
		SetupCents int64  `json:"setup_cents"`
		IsDefault  bool   `json:"is_default"`
		SortWeight int    `json:"sort_weight"`
	} `json:"values"`
}

func (in adminConfigOptionInput) validate() error {
	if strings.TrimSpace(in.Name) == "" {
		return fmt.Errorf("请填写配置项名称")
	}
	if in.OptionType < 1 || in.OptionType > 4 {
		return fmt.Errorf("配置项类型必须是 1 下拉 / 2 单选 / 3 开关 / 4 数量")
	}
	if len(in.Values) == 0 {
		return fmt.Errorf("请至少添加一个候选项")
	}
	if in.OptionType == 4 && len(in.Values) != 1 {
		return fmt.Errorf("数量型配置项只能有一个计价单位")
	}
	for _, v := range in.Values {
		if strings.TrimSpace(v.Label) == "" {
			return fmt.Errorf("候选项名称不能为空")
		}
		if v.PriceCents < 0 || v.SetupCents < 0 {
			return fmt.Errorf("价格不能为负数")
		}
	}
	if in.QtyMax > 0 && in.QtyMin > in.QtyMax {
		return fmt.Errorf("数量下限不能大于上限")
	}
	return nil
}

func (in adminConfigOptionInput) toStoreInput() store.ConfigOptionInput {
	out := store.ConfigOptionInput{
		Name:        strings.TrimSpace(in.Name),
		Description: strings.TrimSpace(in.Description),
		OptionType:  in.OptionType,
		Required:    in.Required,
		SortWeight:  in.SortWeight,
		QtyMin:      in.QtyMin,
		QtyMax:      in.QtyMax,
	}
	for _, v := range in.Values {
		out.Values = append(out.Values, store.ConfigValueInput{
			Label:      strings.TrimSpace(v.Label),
			PriceCents: v.PriceCents,
			SetupCents: v.SetupCents,
			IsDefault:  v.IsDefault,
			SortWeight: v.SortWeight,
		})
	}
	return out
}

// ---- 商品自定义字段（魔方 customfields type=product）----

func (a *App) adminListCustomFields(c *gin.Context) {
	items, err := a.Store.ListProductCustomFields(c, c.Param("id"))
	if err != nil {
		httpx.Fail(c, 500, "CUSTOM_FIELDS_FAILED", "读取自定义字段失败")
		return
	}
	httpx.OK(c, 200, items)
}

func (a *App) adminCreateCustomField(c *gin.Context) {
	p, _ := getPrincipal(c)
	var in struct {
		Name        string   `json:"name"`
		FieldKey    string   `json:"field_key"`
		FieldType   string   `json:"field_type"`
		Options     []string `json:"options"`
		Description string   `json:"description"`
		Placeholder string   `json:"placeholder"`
		Required    bool     `json:"required"`
		AdminOnly   bool     `json:"admin_only"`
		ShowOnOrder *bool    `json:"show_on_order"`
		Regex       string   `json:"regex"`
		SortWeight  int      `json:"sort_weight"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	in.FieldKey = strings.TrimSpace(in.FieldKey)
	if in.Name == "" || in.FieldKey == "" {
		httpx.Fail(c, 400, "INVALID_CUSTOM_FIELD", "字段名与字段键都不能为空")
		return
	}
	switch in.FieldType {
	case "text", "textarea", "dropdown", "password":
	case "":
		in.FieldType = "text"
	default:
		httpx.Fail(c, 400, "INVALID_CUSTOM_FIELD", "字段类型仅支持 text / textarea / dropdown / password")
		return
	}
	if in.FieldType == "dropdown" && len(in.Options) == 0 {
		httpx.Fail(c, 400, "INVALID_CUSTOM_FIELD", "下拉字段至少要有一个候选项")
		return
	}
	if strings.TrimSpace(in.Regex) != "" {
		if _, err := regexp.Compile(in.Regex); err != nil {
			httpx.Fail(c, 400, "INVALID_CUSTOM_FIELD", "校验正则无效："+err.Error())
			return
		}
	}
	showOnOrder := true
	if in.ShowOnOrder != nil {
		showOnOrder = *in.ShowOnOrder
	}
	f, err := a.Store.CreateProductCustomField(c, c.Param("id"), store.CustomFieldInput{
		Name:        in.Name,
		FieldKey:    in.FieldKey,
		FieldType:   in.FieldType,
		Options:     in.Options,
		Description: strings.TrimSpace(in.Description),
		Placeholder: strings.TrimSpace(in.Placeholder),
		Required:    in.Required,
		AdminOnly:   in.AdminOnly,
		ShowOnOrder: showOnOrder,
		Regex:       strings.TrimSpace(in.Regex),
		SortWeight:  in.SortWeight,
	})
	if err != nil {
		httpx.Fail(c, 400, "CUSTOM_FIELD_CREATE_FAILED", err.Error())
		return
	}
	_ = a.Store.Audit(c, p.User.ID, "custom_field.create", "product", c.Param("id"), c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, f)
	httpx.OK(c, 201, f)
}

func (a *App) adminDeleteCustomField(c *gin.Context) {
	p, _ := getPrincipal(c)
	if err := a.Store.DeleteProductCustomField(c, c.Param("field_id")); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpx.Fail(c, 404, "CUSTOM_FIELD_NOT_FOUND", "自定义字段不存在")
			return
		}
		httpx.Fail(c, 400, "CUSTOM_FIELD_DELETE_FAILED", err.Error())
		return
	}
	_ = a.Store.Audit(c, p.User.ID, "custom_field.delete", "custom_field", c.Param("field_id"), c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, nil)
	httpx.OK(c, 200, map[string]bool{"ok": true})
}

// ---- 前台：下单页读取商品的配置项与自定义字段 ----

// productConfig 返回买家下单时需要渲染的配置项、自定义字段与库存信息。
// 只暴露 show_on_order 的字段，管理员专属字段不下发。
func (a *App) productConfig(c *gin.Context) {
	productID := c.Param("id")
	options, err := a.Store.ListProductConfigOptions(c, productID)
	if err != nil {
		httpx.Fail(c, 500, "PRODUCT_CONFIG_FAILED", "读取配置项失败")
		return
	}
	fields, err := a.Store.ListProductCustomFields(c, productID)
	if err != nil {
		httpx.Fail(c, 500, "PRODUCT_CONFIG_FAILED", "读取自定义字段失败")
		return
	}
	visible := make([]model.ProductCustomField, 0, len(fields))
	for _, f := range fields {
		if f.AdminOnly || !f.ShowOnOrder {
			continue
		}
		visible = append(visible, f)
	}
	stock, err := a.Store.ProductStock(c, productID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpx.Fail(c, 404, "PRODUCT_NOT_FOUND", "商品不存在")
			return
		}
		httpx.Fail(c, 500, "PRODUCT_CONFIG_FAILED", "读取库存失败")
		return
	}
	links, err := a.Store.ListProductConfigLinks(c, productID)
	if err != nil {
		httpx.Fail(c, 500, "PRODUCT_CONFIG_FAILED", "读取配置项联动失败")
		return
	}
	httpx.OK(c, 200, map[string]any{
		"config_options":   options,
		"config_links":     links,
		"custom_fields":    visible,
		"stock_control":    stock.StockControl,
		"available":        stock.Available,
		"allow_qty":        stock.AllowQty,
		"max_per_customer": stock.MaxPerCustomer,
	})
}

// ---- 配置项条件联动 ----

func (a *App) adminListConfigLinks(c *gin.Context) {
	links, err := a.Store.ListProductConfigLinks(c, c.Param("id"))
	if err != nil {
		httpx.Fail(c, 500, "CONFIG_LINKS_FAILED", "读取联动规则失败")
		return
	}
	httpx.OK(c, 200, links)
}

// adminCreateConfigLink 新建一条联动规则：source 选中 source_value 时，
// target 才可见/必填。
func (a *App) adminCreateConfigLink(c *gin.Context) {
	pr, _ := getPrincipal(c)
	var in struct {
		SourceOptionID string `json:"source_option_id"`
		SourceValueID  string `json:"source_value_id"`
		TargetOptionID string `json:"target_option_id"`
		Visible        *bool  `json:"visible"`
		Required       bool   `json:"required"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	for _, v := range []string{in.SourceOptionID, in.SourceValueID, in.TargetOptionID} {
		if strings.TrimSpace(v) == "" {
			httpx.Fail(c, 400, "INVALID_CONFIG_LINK", "来源配置项、触发候选值与目标配置项都不能为空")
			return
		}
	}
	visible := true
	if in.Visible != nil {
		visible = *in.Visible
	}
	link, err := a.Store.SetConfigLink(c, c.Param("id"), in.SourceOptionID, in.SourceValueID, in.TargetOptionID, visible, in.Required)
	if errors.Is(err, store.ErrNotFound) {
		httpx.Fail(c, 404, "CONFIG_LINK_TARGET_NOT_FOUND", "来源或目标配置项不属于该商品")
		return
	}
	if err != nil {
		httpx.Fail(c, 400, "CONFIG_LINK_CREATE_FAILED", err.Error())
		return
	}
	_ = a.Store.Audit(c, pr.User.ID, "config_link.create", "product", c.Param("id"), c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, link)
	httpx.OK(c, 201, link)
}

// adminListProductPrices 列出商品在每个（周期，币种）组合上的价格。
// 多币种独立定价下，同一个商品可以有不按汇率折算的独立价格，
// 后台需要看到完整矩阵而不是单一行。
func (a *App) adminListProductPrices(c *gin.Context) {
	prices, err := a.Store.ListProductPriceCurrencies(c, c.Param("id"))
	if err != nil {
		httpx.Fail(c, 500, "PRODUCT_PRICES_FAILED", "读取商品价格失败")
		return
	}
	httpx.OK(c, 200, prices)
}

// productPriceCurrencies 列出商品在每个（周期，币种）上的价格，供前台切换结算币种。
func (a *App) productPriceCurrencies(c *gin.Context) {
	prices, err := a.Store.ListProductPriceCurrencies(c, c.Param("id"))
	if err != nil {
		httpx.Fail(c, 500, "PRODUCT_PRICES_FAILED", "读取商品价格失败")
		return
	}
	httpx.OK(c, 200, prices)
}
