package store

import (
	"context"
	"errors"
	"strings"
)

// 商品下拉优化（对齐魔方 CBAP ProductDropDownSelect 插件）。
//
// 插件只有一个配置面：后台选择「产品（服务）信息详情页的商品下拉框」的下拉样式
// （default 平铺 / first_group 一级分组 / second_group 二级分组 / first_second_group
// 一级 + 二级分组），用户端在打开下拉框时按该样式渲染。
//
// 站内等价物是「服务 → 升降级」弹窗里选择目标商品的下拉框。本站商品分组只有一级
// （product_groups），因此 second_group / first_second_group 的呈现与 first_group 相同
// ——都按商品分组聚合（分组为一级、商品为二级叶子）；样式值按插件原样保存，
// 站内将来出现多级分组时无需迁移。

// ProductDropDownStyles 是插件支持的四类下拉样式。
var ProductDropDownStyles = []string{"default", "first_group", "second_group", "first_second_group"}

// ProductDropDownConfig 是商品下拉样式配置（system_settings 键 product_drop_down_select）。
type ProductDropDownConfig struct {
	Style string `json:"style"`
}

// GetProductDropDownConfig 读取下拉样式；未配置时返回 default。
func (s *Store) GetProductDropDownConfig(ctx context.Context) (ProductDropDownConfig, error) {
	out := ProductDropDownConfig{Style: "default"}
	err := s.settingGet(ctx, "product_drop_down_select", &out)
	if errors.Is(err, ErrNotFound) {
		return ProductDropDownConfig{Style: "default"}, nil
	}
	if err != nil {
		return ProductDropDownConfig{}, err
	}
	if !validProductDropDownStyle(out.Style) {
		out.Style = "default"
	}
	return out, nil
}

// SaveProductDropDownConfig 保存下拉样式；非法样式被拒绝而不是静默归位。
func (s *Store) SaveProductDropDownConfig(ctx context.Context, style string) error {
	if !validProductDropDownStyle(style) {
		return ErrInvalidState
	}
	return s.settingSave(ctx, "product_drop_down_select", ProductDropDownConfig{Style: style})
}

func validProductDropDownStyle(style string) bool {
	for _, v := range ProductDropDownStyles {
		if v == style {
			return true
		}
	}
	return false
}

// ProductDropDownGroup 是按商品分组聚合的一段下拉选项（未分组的商品落「未分组」）。
type ProductDropDownGroup struct {
	Group    string   `json:"group"`
	Products []string `json:"products"`
}

// ListProductDropDownGroups 返回在售商品按分组聚合的视图（启用 + 未删除）。
func (s *Store) ListProductDropDownGroups(ctx context.Context) ([]ProductDropDownGroup, error) {
	rows, err := s.DB.Query(ctx, `SELECT coalesce(g.name,''),p.name
FROM products p LEFT JOIN product_groups g ON g.id=p.group_id
WHERE p.active=true AND p.deleted_at IS NULL
ORDER BY coalesce(g.sort_weight,0) DESC, g.name, p.sort_weight DESC, p.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	index := map[string]int{}
	out := []ProductDropDownGroup{}
	for rows.Next() {
		var group, name string
		if err := rows.Scan(&group, &name); err != nil {
			return nil, err
		}
		if strings.TrimSpace(group) == "" {
			group = "未分组"
		}
		i, ok := index[group]
		if !ok {
			out = append(out, ProductDropDownGroup{Group: group, Products: []string{}})
			i = len(out) - 1
			index[group] = i
		}
		out[i].Products = append(out[i].Products, name)
	}
	return out, rows.Err()
}
