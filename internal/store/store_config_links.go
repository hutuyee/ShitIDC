package store

import (
	"context"
	"fmt"

	"github.com/hutuyee/ShitIDC/internal/model"
)

// ---- 配置项条件联动（魔方 product_config_options_links）----
//
// 规则语义：source 配置项选中了 source_value 时，target 配置项才可见
// （visible），并可被提升为必填（required）。一个 target 可以有多条规则，
// 命中任意一条即生效。全部用公开 ID 表达，前端可直接用。

// ConfigLink 是一条联动规则。
type ConfigLink struct {
	SourceOptionID string `json:"source_option_id"`
	SourceValueID  string `json:"source_value_id"`
	TargetOptionID string `json:"target_option_id"`
	Visible        bool   `json:"visible"`
	Required       bool   `json:"required"`
}

// ListProductConfigLinks 返回商品相关的全部联动规则。
func (s *Store) ListProductConfigLinks(ctx context.Context, productPublicID string) ([]ConfigLink, error) {
	return listProductConfigLinksQ(ctx, s.DB, productPublicID)
}

// listProductConfigLinksQ 是 ListProductConfigLinks 的可复用实现，
// 既接受连接池也接受订单事务。
func listProductConfigLinksQ(ctx context.Context, q querior, productPublicID string) ([]ConfigLink, error) {
	rows, err := q.Query(ctx, `SELECT so.public_id::text, sv.public_id::text, t.public_id::text, l.visible, l.required
FROM config_option_links l
JOIN config_options so ON so.id=l.source_option_id
JOIN config_option_values sv ON sv.id=l.source_value_id
JOIN config_options t ON t.id=l.target_option_id
JOIN products p ON p.public_id=$1
LEFT JOIN config_group_links gl ON gl.group_id=t.group_id AND gl.product_id=p.id
WHERE t.product_id=p.id OR (t.group_id IS NOT NULL AND gl.id IS NOT NULL)`, productPublicID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ConfigLink{}
	for rows.Next() {
		var l ConfigLink
		if err := rows.Scan(&l.SourceOptionID, &l.SourceValueID, &l.TargetOptionID, &l.Visible, &l.Required); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// SetConfigLink 新建或覆盖一条联动规则。
func (s *Store) SetConfigLink(ctx context.Context, productPublicID, sourceOptionPublic, sourceValuePublic, targetOptionPublic string, visible, required bool) (ConfigLink, error) {
	var sourceOptionID, sourceValueID, targetOptionID int64
	err := s.DB.QueryRow(ctx, `SELECT o.id,v.id
FROM config_options o
JOIN config_option_values v ON v.option_id=o.id
JOIN products p ON p.public_id=$1
LEFT JOIN config_group_links gl ON gl.group_id=o.group_id AND gl.product_id=p.id
WHERE o.public_id=$2 AND v.public_id=$3 AND (o.product_id=p.id OR (o.group_id IS NOT NULL AND gl.id IS NOT NULL))`, productPublicID, sourceOptionPublic, sourceValuePublic).Scan(&sourceOptionID, &sourceValueID)
	if err != nil {
		return ConfigLink{}, ErrNotFound
	}
	err = s.DB.QueryRow(ctx, `SELECT t.id FROM config_options t
JOIN products p ON p.public_id=$1
LEFT JOIN config_group_links gl ON gl.group_id=t.group_id AND gl.product_id=p.id
WHERE t.public_id=$2 AND (t.product_id=p.id OR (t.group_id IS NOT NULL AND gl.id IS NOT NULL))`, productPublicID, targetOptionPublic).Scan(&targetOptionID)
	if err != nil {
		return ConfigLink{}, ErrNotFound
	}
	if sourceOptionID == targetOptionID {
		return ConfigLink{}, fmt.Errorf("不能让配置项依赖它自己")
	}
	if _, err := s.DB.Exec(ctx, `INSERT INTO config_option_links(source_option_id,source_value_id,target_option_id,visible,required)
VALUES($1,$2,$3,$4,$5)
ON CONFLICT (source_value_id,target_option_id) DO UPDATE SET visible=excluded.visible,required=excluded.required`,
		sourceOptionID, sourceValueID, targetOptionID, visible, required); err != nil {
		return ConfigLink{}, err
	}
	return ConfigLink{
		SourceOptionID: sourceOptionPublic,
		SourceValueID:  sourceValuePublic,
		TargetOptionID: targetOptionPublic,
		Visible:        visible,
		Required:       required,
	}, nil
}

// DeleteConfigLink 删除一条联动规则。
func (s *Store) DeleteConfigLink(ctx context.Context, linkPublicID string) error {
	tag, err := s.DB.Exec(ctx, `DELETE FROM config_option_links WHERE public_id=$1`, linkPublicID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// resolveVisibility 根据已选中的配置项，算出每个配置项最终的可见性与必填性。
// 没有任何联动规则指向的配置项永远可见，保持原有 required。
func resolveVisibility(options []model.ConfigOption, links []ConfigLink, choices []ConfigChoice) (map[string]bool, map[string]bool) {
	chosen := map[string]string{}
	for _, c := range choices {
		chosen[c.OptionID] = c.ValueID
	}
	visible := map[string]bool{}
	required := map[string]bool{}
	for _, o := range options {
		visible[o.PublicID] = true
		required[o.PublicID] = o.Required
	}
	for _, l := range links {
		if chosen[l.SourceOptionID] != l.SourceValueID {
			continue
		}
		visible[l.TargetOptionID] = l.Visible
		if l.Required {
			required[l.TargetOptionID] = true
		}
	}
	return visible, required
}
