package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// 开通上下文：把订单里买家选择的配置项 / 自定义字段解析成「传给 Provider 的键 → 值」。
//
// 对应魔方的 $params['configoptions'] 与 $params['customfields']：
//   - 配置项的键取 config_options.provider_key（导入魔方插件后把商品的配置项
//     挂上同样的 key，如 site_max）；provider_key 为空时回退用配置项名称。
//   - 自定义字段本来就带 field_key（product_custom_fields 的设计约定），直接用。
//
// 值的语义：下拉/单选传买家选中的子项标签；开关传 1/0（魔方 yesno 约定）；
// 数量型传数量。

// ProvisionContext 是开通一个服务所需的业务上下文。
type ProvisionContext struct {
	UserEmail     string
	Quantity      int
	ConfigOptions map[string]string
	CustomFields  map[string]string
}

// GetServiceProvisionContext 读取并解析一个服务的开通上下文。
// 没有关联订单明细（手工开的旧服务）时返回空配置，不报错。
func (s *Store) GetServiceProvisionContext(ctx context.Context, servicePublicID string) (ProvisionContext, error) {
	out := ProvisionContext{ConfigOptions: map[string]string{}, CustomFields: map[string]string{}}
	var email string
	var quantity int
	var selectionsRaw, customRaw []byte
	err := s.DB.QueryRow(ctx, `
SELECT u.email, coalesce(oi.quantity,1), coalesce(oi.config_selections::text,''), coalesce(oi.custom_fields::text,'')
FROM services s
JOIN users u ON u.id=s.user_id
LEFT JOIN order_items oi ON oi.id=s.order_item_id
WHERE s.public_id=$1`, servicePublicID).Scan(&email, &quantity, &selectionsRaw, &customRaw)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, ErrNotFound
	}
	if err != nil {
		return out, err
	}
	out.UserEmail = email
	out.Quantity = quantity

	// 自定义字段：JSON 对象 {field_key: value}
	if len(customRaw) > 0 {
		cf := map[string]string{}
		if err := json.Unmarshal(customRaw, &cf); err == nil {
			out.CustomFields = cf
		}
	}

	// 配置项选择：[{option_id, value_id, quantity}]
	type choice struct {
		OptionID string `json:"option_id"`
		ValueID  string `json:"value_id"`
		Quantity int    `json:"quantity"`
	}
	choices := []choice{}
	if len(selectionsRaw) > 0 && string(selectionsRaw) != "[]" {
		if err := json.Unmarshal(selectionsRaw, &choices); err != nil {
			return out, fmt.Errorf("解析 config_selections 失败: %w", err)
		}
	}
	if len(choices) == 0 {
		return out, nil
	}

	// 一次性取回涉及的配置项与子项，避免逐条查询。
	optIDs := make([]string, 0, len(choices))
	valIDs := make([]string, 0, len(choices))
	for _, c := range choices {
		if c.OptionID != "" {
			optIDs = append(optIDs, c.OptionID)
		}
		if c.ValueID != "" {
			valIDs = append(valIDs, c.ValueID)
		}
	}
	type optRow struct {
		Name        string
		ProviderKey string
		OptionType  int
	}
	opts := map[string]optRow{} // public_id → row
	if len(optIDs) > 0 {
		rows, err := s.DB.Query(ctx, `SELECT public_id::text,name,provider_key,option_type FROM config_options WHERE public_id = ANY($1)`, optIDs)
		if err != nil {
			return out, err
		}
		defer rows.Close()
		for rows.Next() {
			var id, name, pkey string
			var t int
			if err := rows.Scan(&id, &name, &pkey, &t); err != nil {
				return out, err
			}
			opts[id] = optRow{Name: name, ProviderKey: pkey, OptionType: t}
		}
		if err := rows.Err(); err != nil {
			return out, err
		}
	}
	labels := map[string]string{} // value public_id → label
	if len(valIDs) > 0 {
		rows, err := s.DB.Query(ctx, `SELECT public_id::text,label FROM config_option_values WHERE public_id = ANY($1)`, valIDs)
		if err != nil {
			return out, err
		}
		defer rows.Close()
		for rows.Next() {
			var id, label string
			if err := rows.Scan(&id, &label); err != nil {
				return out, err
			}
			labels[id] = label
		}
		if err := rows.Err(); err != nil {
			return out, err
		}
	}

	keyCount := map[string]int{} // 键名冲突检测：同名键后者覆盖前加后缀
	for _, c := range choices {
		o, ok := opts[c.OptionID]
		if !ok {
			continue // 配置项已被删除：跳过，不影响开通
		}
		key := o.ProviderKey
		if key == "" {
			key = o.Name
		}
		if key == "" {
			continue
		}
		keyCount[key]++
		if keyCount[key] > 1 {
			key = fmt.Sprintf("%s_%d", key, keyCount[key])
		}
		switch o.OptionType {
		case 3: // 开关：魔方 yesno 约定 1/0
			if c.ValueID != "" {
				out.ConfigOptions[key] = "1"
			} else {
				out.ConfigOptions[key] = "0"
			}
		case 4: // 数量
			if c.Quantity > 0 {
				out.ConfigOptions[key] = fmt.Sprint(c.Quantity)
			}
		default: // 下拉 / 单选：选中子项的标签
			if label := labels[c.ValueID]; label != "" {
				out.ConfigOptions[key] = label
			}
		}
	}
	return out, nil
}
