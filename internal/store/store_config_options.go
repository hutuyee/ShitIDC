package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/hutuyee/ShitIDC/internal/model"
)

// querior 让同一段查询既能在连接池上跑（管理端读取），也能在订单事务里跑。
// 这一点很关键：订单事务里如果再去连接池取第二条连接，20 个并发下单就会把
// 连接池占满并互相等待（集成测试 TestConcurrentWalletDeduction 正是这样卡住的）。
type querior interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// 商品配置项 / 自定义字段 / 库存限购（魔方可配置选项的 Go 实现）。
//
// 与魔方一致的四类配置项：1 下拉、2 单选、3 开关、4 数量。价格模型比魔方简单：
// 子项上直接带“相对商品基础价的加价”（price_cents）与一次性初装费（setup_cents），
// 不再走 shd_pricing 那张多态表。

// ListProductConfigOptions 返回商品的可配置项及其候选项，
// 包含通过配置组关联过来的配置项。
func (s *Store) ListProductConfigOptions(ctx context.Context, productPublicID string) ([]model.ConfigOption, error) {
	return listProductConfigOptionsQ(ctx, s.DB, productPublicID)
}

// listProductConfigOptionsQ 是 ListProductConfigOptions 的可复用实现，
// 既接受连接池也接受订单事务。
func listProductConfigOptionsQ(ctx context.Context, q querior, productPublicID string) ([]model.ConfigOption, error) {
	rows, err := q.Query(ctx, `SELECT o.public_id::text,o.name,o.description,o.option_type,o.required,o.sort_weight,o.qty_min,o.qty_max
FROM config_options o
JOIN products p ON p.public_id=$1
LEFT JOIN config_group_links l ON l.group_id=o.group_id AND l.product_id=p.id
WHERE o.product_id=p.id OR (o.group_id IS NOT NULL AND l.id IS NOT NULL)
ORDER BY o.sort_weight,o.id`, productPublicID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	opts := []model.ConfigOption{}
	for rows.Next() {
		var o model.ConfigOption
		if err := rows.Scan(&o.PublicID, &o.Name, &o.Description, &o.OptionType, &o.Required, &o.SortWeight, &o.QtyMin, &o.QtyMax); err != nil {
			return nil, err
		}
		o.Values = []model.ConfigOptionValue{}
		opts = append(opts, o)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(opts) == 0 {
		return opts, nil
	}
	// 一次性把子项取回来，避免 N+1。
	ids := make([]string, 0, len(opts))
	for _, o := range opts {
		ids = append(ids, o.PublicID)
	}
	vrows, err := q.Query(ctx, `SELECT v.public_id::text,v.label,v.price_cents,v.setup_cents,v.is_default,v.hidden,v.sort_weight,o.public_id::text
FROM config_option_values v
JOIN config_options o ON o.id=v.option_id
WHERE o.public_id = ANY($1::uuid[]) AND v.hidden=false
ORDER BY v.sort_weight,v.id`, ids)
	if err != nil {
		return nil, err
	}
	defer vrows.Close()
	byOption := map[string][]model.ConfigOptionValue{}
	for vrows.Next() {
		var v model.ConfigOptionValue
		var optionID string
		if err := vrows.Scan(&v.PublicID, &v.Label, &v.PriceCents, &v.SetupCents, &v.IsDefault, &v.Hidden, &v.SortWeight, &optionID); err != nil {
			return nil, err
		}
		byOption[optionID] = append(byOption[optionID], v)
	}
	if err := vrows.Err(); err != nil {
		return nil, err
	}
	for i := range opts {
		if vals, ok := byOption[opts[i].PublicID]; ok {
			opts[i].Values = vals
		}
	}
	return opts, nil
}

// ListProductCustomFields 返回商品的自定义字段（魔方 customfields type=product）。
func (s *Store) ListProductCustomFields(ctx context.Context, productPublicID string) ([]model.ProductCustomField, error) {
	return listProductCustomFieldsQ(ctx, s.DB, productPublicID)
}

// listProductCustomFieldsQ 是 ListProductCustomFields 的可复用实现。
func listProductCustomFieldsQ(ctx context.Context, q querior, productPublicID string) ([]model.ProductCustomField, error) {
	rows, err := q.Query(ctx, `SELECT f.public_id::text,f.name,f.field_key,f.field_type,f.options,f.description,f.placeholder,f.required,f.admin_only,f.show_on_order,f.regex,f.sort_weight
FROM product_custom_fields f JOIN products p ON p.id=f.product_id
WHERE p.public_id=$1 ORDER BY f.sort_weight,f.id`, productPublicID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.ProductCustomField{}
	for rows.Next() {
		var f model.ProductCustomField
		if err := rows.Scan(&f.PublicID, &f.Name, &f.FieldKey, &f.FieldType, &f.Options, &f.Description, &f.Placeholder, &f.Required, &f.AdminOnly, &f.ShowOnOrder, &f.Regex, &f.SortWeight); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// ---- 配置项价格计算 ----

// 配置项类型，与魔方 shd_product_config_options.option_type 一致。
const (
	ConfigTypeDropdown = 1
	ConfigTypeRadio    = 2
	ConfigTypeYesNo    = 3
	ConfigTypeQuantity = 4
)

// ConfigChoice 是买家针对一个配置项提交的选择。
//   - 下拉/单选/开关：填 ValueID
//   - 数量型：填 Quantity（等价于把唯一子项买 N 份）
type ConfigChoice struct {
	OptionID string `json:"option_id"`
	ValueID  string `json:"value_id"`
	Quantity int    `json:"quantity"`
}

// ConfigPricing 是一次报价的结果。
type ConfigPricing struct {
	Selections  []model.ConfigSelection
	ConfigCents int64 // 所有配置项的加价小计（不含初装费）
	SetupCents  int64 // 所有配置项的一次性初装费
	CycleCents  int64 // 这些加价按周期计费的小计（本次实现与 ConfigCents 相同）
}

// ResolveConfigSelection 校验买家选择并算出加价。服务端唯一的计价入口：
// 前端的报价只用于展示，下单时一定用这里的重算结果。
//
// 校验规则：
//   - 未知的配置项 ID / 子项 ID 直接拒绝（防止越权改价）
//   - required 的配置项必须选，数量型必须落在 [qty_min, qty_max]
//   - 开关型：选了子项表示“开”；没选表示“关”，加价为 0
//   - 数量型：数量 × 该子项单价
func ResolveConfigSelection(options []model.ConfigOption, choices []ConfigChoice) (ConfigPricing, error) {
	return ResolveConfigSelectionWithLinks(options, nil, choices)
}

// ResolveConfigSelectionWithLinks 是带条件联动的版本：先按已选值算出每个配置项
// 最终的可见性与必填性，再照常计价。两条关键规则：
//   - 被联动隐藏的配置项即使前端传了值也会被忽略（不收费，防止塞隐藏项加价）
//   - 被联动提升为必填的配置项必须选
func ResolveConfigSelectionWithLinks(options []model.ConfigOption, links []ConfigLink, choices []ConfigChoice) (ConfigPricing, error) {
	visible, required := resolveVisibility(options, links, choices)
	choiceByOption := map[string]ConfigChoice{}
	for _, c := range choices {
		id := strings.TrimSpace(c.OptionID)
		if id == "" {
			continue
		}
		choiceByOption[id] = c
	}

	out := ConfigPricing{Selections: []model.ConfigSelection{}}
	for _, opt := range options {
		isVisible := visible[opt.PublicID]
		isRequired := required[opt.PublicID]
		choice, provided := choiceByOption[opt.PublicID]
		if !isVisible {
			// 隐藏项直接跳过：既不校验必填，也不计费。
			provided = false
			choice = ConfigChoice{}
		}
		selected := provided && (strings.TrimSpace(choice.ValueID) != "" || choice.Quantity > 0)

		if !selected {
			// 数量型与开关型有默认值：数量型默认下限，开关型默认关（不加价）。
			if opt.OptionType == ConfigTypeQuantity {
				if isRequired {
					qty := opt.QtyMin
					if qty < 1 {
						qty = 1
					}
					if qty > opt.QtyMax && opt.QtyMax > 0 {
						return ConfigPricing{}, fmt.Errorf("配置项「%s」的数量必须是 %d-%d", opt.Name, opt.QtyMin, opt.QtyMax)
					}
					if err := applyQuantity(&out, opt, qty); err != nil {
						return ConfigPricing{}, err
					}
					continue
				}
				continue
			}
			if opt.OptionType == ConfigTypeYesNo {
				// 未选择 = 关闭，不加价。
				out.Selections = append(out.Selections, model.ConfigSelection{
					OptionID: opt.PublicID, OptionName: opt.Name, ValueLabel: "否",
				})
				continue
			}
			if isRequired {
				return ConfigPricing{}, fmt.Errorf("请选择「%s」", opt.Name)
			}
			continue
		}

		switch opt.OptionType {
		case ConfigTypeQuantity:
			if err := applyQuantity(&out, opt, choice.Quantity); err != nil {
				return ConfigPricing{}, err
			}
		default:
			value, ok := findConfigValue(opt, choice.ValueID)
			if !ok {
				return ConfigPricing{}, fmt.Errorf("「%s」的选项无效", opt.Name)
			}
			out.ConfigCents += value.PriceCents
			out.SetupCents += value.SetupCents
			out.Selections = append(out.Selections, model.ConfigSelection{
				OptionID:   opt.PublicID,
				OptionName: opt.Name,
				ValueID:    value.PublicID,
				ValueLabel: value.Label,
				PriceCents: value.PriceCents,
			})
		}
	}
	out.CycleCents = out.ConfigCents
	return out, nil
}

// applyQuantity 处理数量型配置项：数量必须在范围内，加价 = 数量 × 子项单价。
func applyQuantity(out *ConfigPricing, opt model.ConfigOption, qty int) error {
	if len(opt.Values) == 0 {
		return fmt.Errorf("配置项「%s」没有可用的计价单位", opt.Name)
	}
	min := opt.QtyMin
	if min < 1 {
		min = 1
	}
	max := opt.QtyMax
	if qty < min || (max > 0 && qty > max) {
		if max > 0 {
			return fmt.Errorf("配置项「%s」的数量必须是 %d-%d", opt.Name, min, max)
		}
		return fmt.Errorf("配置项「%s」的数量不能小于 %d", opt.Name, min)
	}
	unit := opt.Values[0]
	subtotal := unit.PriceCents * int64(qty)
	out.ConfigCents += subtotal
	out.SetupCents += unit.SetupCents
	out.Selections = append(out.Selections, model.ConfigSelection{
		OptionID:   opt.PublicID,
		OptionName: opt.Name,
		ValueID:    unit.PublicID,
		ValueLabel: unit.Label,
		Quantity:   qty,
		PriceCents: subtotal,
	})
	return nil
}

// findConfigValue 按公开 ID 查子项，顺带挡住不属于该配置项的 ID。
func findConfigValue(opt model.ConfigOption, valueID string) (model.ConfigOptionValue, bool) {
	valueID = strings.TrimSpace(valueID)
	if valueID == "" {
		return model.ConfigOptionValue{}, false
	}
	for _, v := range opt.Values {
		if v.PublicID == valueID {
			return v, true
		}
	}
	return model.ConfigOptionValue{}, false
}

// ---- 下单时的配置项 / 自定义字段 / 库存校验（全部在订单事务内完成）----

// OrderConfigInput 是买家在配置项与自定义字段上的选择。
type OrderConfigInput struct {
	Choices      []ConfigChoice    `json:"config"`
	CustomFields map[string]string `json:"custom_fields"`
}

// resolveConfigTx 在事务内完成三件事：
//  1. 读出商品可见的配置项（含配置组继承）与自定义字段
//  2. 用 ResolveConfigSelection 校验并算出配置加价（服务端唯一计价入口）
//  3. 校验自定义字段（必填、正则、下拉候选、管理员专属字段）
//  3. 校验自定义字段（必填、正则、下拉候选、管理员专属字段）
func (s *Store) resolveConfigTx(ctx context.Context, tx pgx.Tx, productPublicID string, in OrderConfigInput) (ConfigPricing, string, string, error) {
	// 全部走订单事务自己的连接：不能再向连接池借连接，否则并发下单会自锁。
	options, err := listProductConfigOptionsQ(ctx, tx, productPublicID)
	if err != nil {
		return ConfigPricing{}, "", "", err
	}
	links, err := listProductConfigLinksQ(ctx, tx, productPublicID)
	if err != nil {
		return ConfigPricing{}, "", "", err
	}
	pricing, err := ResolveConfigSelectionWithLinks(options, links, in.Choices)
	if err != nil {
		return ConfigPricing{}, "", "", err
	}
	selectionsJSON, err := json.Marshal(pricing.Selections)
	if err != nil {
		return ConfigPricing{}, "", "", err
	}
	fields, err := listProductCustomFieldsQ(ctx, tx, productPublicID)
	if err != nil {
		return ConfigPricing{}, "", "", err
	}
	clean, err := validateCustomFields(fields, in.CustomFields)
	if err != nil {
		return ConfigPricing{}, "", "", err
	}
	fieldsJSON, err := json.Marshal(clean)
	if err != nil {
		return ConfigPricing{}, "", "", err
	}
	// 注意：返回 string 而不是 []byte。pqx 把 []byte 当作 bytea 编码，写进 jsonb
	// 列会报 "invalid input syntax for type json"；string 才会作为 JSON 文本发送。
	return pricing, string(selectionsJSON), string(fieldsJSON), nil
}

// validateCustomFields 校验并归一化买家提交的自定义字段。
// 只接受商品声明过的 field_key，避免把任意键值塞进开通请求。
func validateCustomFields(fields []model.ProductCustomField, input map[string]string) (map[string]string, error) {
	out := map[string]string{}
	for _, f := range fields {
		if f.AdminOnly {
			continue // 管理员专属字段不接受买家提交
		}
		raw := strings.TrimSpace(input[f.FieldKey])
		if raw == "" {
			if f.Required {
				return nil, fmt.Errorf("请填写「%s」", f.Name)
			}
			continue
		}
		if len(raw) > 2000 {
			return nil, fmt.Errorf("「%s」内容过长", f.Name)
		}
		switch f.FieldType {
		case "dropdown":
			ok := false
			for _, candidate := range f.Options {
				if candidate == raw {
					ok = true
					break
				}
			}
			if !ok {
				return nil, fmt.Errorf("「%s」的取值不在允许范围内", f.Name)
			}
		}
		if strings.TrimSpace(f.Regex) != "" {
			re, err := regexp.Compile(f.Regex)
			if err != nil {
				return nil, fmt.Errorf("「%s」的校验规则配置有误", f.Name)
			}
			if !re.MatchString(raw) {
				return nil, fmt.Errorf("「%s」格式不正确", f.Name)
			}
		}
		out[f.FieldKey] = raw
	}
	return out, nil
}

// checkStockAndQtyTx 校验单次购买数量、单客户限购，并在开启库存控制时**原子地**
// 扣减库存：
//
//	UPDATE products SET sold_count = sold_count + $n
//	WHERE id=$1 AND stock_control AND stock_qty - sold_count >= $n
//
// 把“校验 + 占用”合并成一条带条件的 UPDATE，行锁由数据库保证，因此并发下单
// 不可能超卖；同时避免了“先 SELECT ... FOR SHARE 再 UPDATE”这种模式在高并发下
// 引发的大量序列化失败。未开启库存控制时不写这一行，也就不与同商品的其他订单
// 争用同一行。
func checkStockAndQtyTx(ctx context.Context, tx pgx.Tx, productInternalID, userID int64, quantity int) error {
	var stockControl, allowQty bool
	var maxPerCustomer int
	err := tx.QueryRow(ctx, `SELECT stock_control,allow_qty,max_per_customer FROM products WHERE id=$1`, productInternalID).
		Scan(&stockControl, &allowQty, &maxPerCustomer)
	if err != nil {
		return err
	}
	if !allowQty && quantity > 1 {
		return fmt.Errorf("该商品一次只能购买 1 件")
	}
	if maxPerCustomer > 0 {
		var owned int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM services s JOIN order_items oi ON oi.id=s.order_item_id
WHERE s.user_id=$1 AND oi.product_id=$2 AND s.status NOT IN ('terminated','failed')`, userID, productInternalID).Scan(&owned); err != nil {
			return err
		}
		if owned+quantity > maxPerCustomer {
			return fmt.Errorf("该商品每位客户最多购买 %d 件（你已有 %d 件）", maxPerCustomer, owned)
		}
	}
	if !stockControl {
		return nil
	}
	var newSold int
	err = tx.QueryRow(ctx, `UPDATE products SET sold_count=sold_count+$2,updated_at=now() WHERE id=$1 AND stock_control AND stock_qty-sold_count >= $2 RETURNING sold_count`, productInternalID, quantity).Scan(&newSold)
	if errors.Is(err, pgx.ErrNoRows) {
		// 条件不成立：要么已售罄，要么剩余不足。再读一次给出准确提示。
		var stockQty, sold int
		if qerr := tx.QueryRow(ctx, `SELECT stock_qty,sold_count FROM products WHERE id=$1`, productInternalID).Scan(&stockQty, &sold); qerr == nil {
			remaining := stockQty - sold
			if remaining <= 0 {
				return fmt.Errorf("该商品已售罄")
			}
			return fmt.Errorf("库存不足，仅剩 %d 件", remaining)
		}
		return fmt.Errorf("库存不足")
	}
	return err
}
