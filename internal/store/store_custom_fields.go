package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/hutuyee/ShitIDC/internal/model"
)

// 商品自定义字段（魔方 shd_customfields，type=product）与库存查询。

// CustomFieldInput 是新建自定义字段的输入。
type CustomFieldInput struct {
	Name        string
	FieldKey    string
	FieldType   string
	Options     []string
	Description string
	Placeholder string
	Required    bool
	AdminOnly   bool
	ShowOnOrder bool
	Regex       string
	SortWeight  int
}

// CreateProductCustomField 为一个商品新增自定义字段。
func (s *Store) CreateProductCustomField(ctx context.Context, productPublicID string, in CustomFieldInput) (model.ProductCustomField, error) {
	var productID int64
	if err := s.DB.QueryRow(ctx, `SELECT id FROM products WHERE public_id=$1`, productPublicID).Scan(&productID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return model.ProductCustomField{}, ErrNotFound
		}
		return model.ProductCustomField{}, err
	}
	if in.Options == nil {
		in.Options = []string{}
	}
	var f model.ProductCustomField
	err := s.DB.QueryRow(ctx, `INSERT INTO product_custom_fields(product_id,name,field_key,field_type,options,description,placeholder,required,admin_only,show_on_order,regex,sort_weight) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12) RETURNING public_id::text,name,field_key,field_type,options,description,placeholder,required,admin_only,show_on_order,regex,sort_weight`,
		productID, in.Name, in.FieldKey, in.FieldType, in.Options, in.Description, in.Placeholder, in.Required, in.AdminOnly, in.ShowOnOrder, in.Regex, in.SortWeight).
		Scan(&f.PublicID, &f.Name, &f.FieldKey, &f.FieldType, &f.Options, &f.Description, &f.Placeholder, &f.Required, &f.AdminOnly, &f.ShowOnOrder, &f.Regex, &f.SortWeight)
	if err != nil {
		if isUniqueViolation(err) {
			return model.ProductCustomField{}, fmt.Errorf("字段键 %s 已存在", in.FieldKey)
		}
		return model.ProductCustomField{}, err
	}
	return f, nil
}

// DeleteProductCustomField 删除一个自定义字段。
func (s *Store) DeleteProductCustomField(ctx context.Context, fieldPublicID string) error {
	tag, err := s.DB.Exec(ctx, `DELETE FROM product_custom_fields WHERE public_id=$1`, fieldPublicID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ProductStock 是商品的可售状态，用于下单页展示与下单前校验。
type ProductStock struct {
	StockControl   bool `json:"stock_control"`
	StockQty       int  `json:"stock_qty"`
	SoldCount      int  `json:"sold_count"`
	Available      int  `json:"available"` // -1 = 不限
	AllowQty       bool `json:"allow_qty"`
	MaxPerCustomer int  `json:"max_per_customer"`
}

// ProductStock 读取库存与限购设置。
func (s *Store) ProductStock(ctx context.Context, productPublicID string) (ProductStock, error) {
	var v ProductStock
	err := s.DB.QueryRow(ctx, `SELECT stock_control,stock_qty,sold_count,allow_qty,max_per_customer FROM products WHERE public_id=$1 AND deleted_at IS NULL`, productPublicID).
		Scan(&v.StockControl, &v.StockQty, &v.SoldCount, &v.AllowQty, &v.MaxPerCustomer)
	if errors.Is(err, pgx.ErrNoRows) {
		return ProductStock{}, ErrNotFound
	}
	if err != nil {
		return ProductStock{}, err
	}
	if v.StockControl {
		v.Available = v.StockQty - v.SoldCount
		if v.Available < 0 {
			v.Available = 0
		}
	} else {
		v.Available = -1
	}
	return v, nil
}
