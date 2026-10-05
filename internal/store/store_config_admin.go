package store

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/hutuyee/ShitIDC/internal/model"
)

// ---- 配置项写入（管理端）----

type ConfigOptionInput struct {
	Name        string
	ProviderKey string // 传给 Provider 的键名（魔方插件导入后用于映射上游参数）
	Description string
	OptionType  int
	Required    bool
	SortWeight  int
	QtyMin      int
	QtyMax      int
	Values      []ConfigValueInput
}

type ConfigValueInput struct {
	Label      string
	PriceCents int64
	SetupCents int64
	IsDefault  bool
	SortWeight int
}

// CreateConfigOption 在一个事务里建配置项与它的候选项。
func (s *Store) CreateConfigOption(ctx context.Context, productPublicID string, in ConfigOptionInput) (model.ConfigOption, error) {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return model.ConfigOption{}, err
	}
	defer tx.Rollback(ctx)
	var productID int64
	if err := tx.QueryRow(ctx, `SELECT id FROM products WHERE public_id=$1`, productPublicID).Scan(&productID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return model.ConfigOption{}, ErrNotFound
		}
		return model.ConfigOption{}, err
	}
	var optID int64
	var optPublic string
	if err := tx.QueryRow(ctx, `INSERT INTO config_options(product_id,name,provider_key,description,option_type,required,sort_weight,qty_min,qty_max) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9) RETURNING id,public_id::text`,
		productID, in.Name, strings.TrimSpace(in.ProviderKey), in.Description, in.OptionType, in.Required, in.SortWeight, maxInt(in.QtyMin, 1), defaultInt(in.QtyMax, 100)).Scan(&optID, &optPublic); err != nil {
		return model.ConfigOption{}, err
	}
	for i, v := range in.Values {
		if _, err := tx.Exec(ctx, `INSERT INTO config_option_values(option_id,label,price_cents,setup_cents,is_default,sort_weight) VALUES($1,$2,$3,$4,$5,$6)`, optID, v.Label, v.PriceCents, v.SetupCents, v.IsDefault, valueSort(v.SortWeight, i)); err != nil {
			return model.ConfigOption{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return model.ConfigOption{}, err
	}
	return s.configOptionByPublicID(ctx, optPublic)
}

// UpdateConfigOption 覆盖配置项与其候选项。候选项整体重建，但**已被订单引用过**的
// 候选项不会真正删除，避免历史订单还原不出买家当时选了什么。
func (s *Store) UpdateConfigOption(ctx context.Context, optionPublicID string, in ConfigOptionInput) error {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var optID int64
	if err := tx.QueryRow(ctx, `SELECT id FROM config_options WHERE public_id=$1`, optionPublicID).Scan(&optID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE config_options SET name=$2,provider_key=$3,description=$4,option_type=$5,required=$6,sort_weight=$7,qty_min=$8,qty_max=$9,updated_at=now() WHERE id=$1`, optID, in.Name, strings.TrimSpace(in.ProviderKey), in.Description, in.OptionType, in.Required, in.SortWeight, maxInt(in.QtyMin, 1), defaultInt(in.QtyMax, 100)); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM config_option_values v WHERE v.option_id=$1 AND NOT EXISTS (SELECT 1 FROM order_items oi WHERE oi.config_selections @> jsonb_build_array(jsonb_build_object('value_id', v.public_id::text)))`, optID); err != nil {
		return err
	}
	for i, v := range in.Values {
		if _, err := tx.Exec(ctx, `INSERT INTO config_option_values(option_id,label,price_cents,setup_cents,is_default,sort_weight) VALUES($1,$2,$3,$4,$5,$6)`, optID, v.Label, v.PriceCents, v.SetupCents, v.IsDefault, valueSort(v.SortWeight, i)); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// DeleteConfigOption 删除配置项；候选项随配置项级联删除。
func (s *Store) DeleteConfigOption(ctx context.Context, optionPublicID string) error {
	tag, err := s.DB.Exec(ctx, `DELETE FROM config_options WHERE public_id=$1`, optionPublicID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// configOptionByPublicID 读回一个配置项（含候选项）。
func (s *Store) configOptionByPublicID(ctx context.Context, optionPublicID string) (model.ConfigOption, error) {
	var o model.ConfigOption
	err := s.DB.QueryRow(ctx, `SELECT public_id::text,name,description,option_type,required,sort_weight,qty_min,qty_max FROM config_options WHERE public_id=$1`, optionPublicID).
		Scan(&o.PublicID, &o.Name, &o.Description, &o.OptionType, &o.Required, &o.SortWeight, &o.QtyMin, &o.QtyMax)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.ConfigOption{}, ErrNotFound
	}
	if err != nil {
		return model.ConfigOption{}, err
	}
	rows, err := s.DB.Query(ctx, `SELECT public_id::text,label,price_cents,setup_cents,is_default,hidden,sort_weight FROM config_option_values WHERE option_id=(SELECT id FROM config_options WHERE public_id=$1) ORDER BY sort_weight,id`, optionPublicID)
	if err != nil {
		return model.ConfigOption{}, err
	}
	defer rows.Close()
	o.Values = []model.ConfigOptionValue{}
	for rows.Next() {
		var v model.ConfigOptionValue
		if err := rows.Scan(&v.PublicID, &v.Label, &v.PriceCents, &v.SetupCents, &v.IsDefault, &v.Hidden, &v.SortWeight); err != nil {
			return model.ConfigOption{}, err
		}
		o.Values = append(o.Values, v)
	}
	return o, rows.Err()
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func defaultInt(v, def int) int {
	if v <= 0 {
		return def
	}
	return v
}

func valueSort(v, fallback int) int {
	if v != 0 {
		return v
	}
	return fallback
}
