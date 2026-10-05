package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// 成本支出（对齐魔方 CBAP 插件 cost_pay）。
//
// 后台在「订单」里登记该订单产生的支出：支出名称、所属主体、金额、支出时间、
// 备注，外加一组可自定义的字段（文本框/下拉，可设必填与列表展示、可排序）。
// 插件在前端的金额单位是「元」小数，本站落库用「分」整数（cost_cents），
// 与仓库其它金额字段口径一致。

// CostPayField 是成本支出的一个自定义字段。
type CostPayField struct {
	PublicID    string    `json:"id"`
	FieldName   string    `json:"field_name"`
	FieldType   string    `json:"field_type"`
	IsRequired  bool      `json:"is_required"`
	FieldOption string    `json:"field_option"`
	ShowList    bool      `json:"show_list"`
	SortWeight  int64     `json:"sort_weight"`
	CreatedAt   time.Time `json:"created_at"`
}

// CostPay 是一条订单支出记录；Values 以字段 public_id 为键。
type CostPay struct {
	PublicID      string            `json:"id"`
	OrderPublicID string            `json:"order_id"`
	Name          string            `json:"name"`
	Owner         string            `json:"owner"`
	CostCents     int64             `json:"cost_cents"`
	CostTime      time.Time         `json:"cost_time"`
	Notes         string            `json:"notes"`
	AdminName     string            `json:"admin_name"`
	CreatedAt     time.Time         `json:"create_time"`
	Values        map[string]string `json:"self_defined_field"`
}

// CostPayFilter 是支出列表的筛选条件（时间均为闭区间）。
type CostPayFilter struct {
	Page            int
	Limit           int
	Keywords        string
	Owner           string
	StartCostTime   *time.Time
	EndCostTime     *time.Time
	StartCreateTime *time.Time
	EndCreateTime   *time.Time
}

// CostPayInput 是新增/修改支出记录的表单。
type CostPayInput struct {
	Name      string
	Owner     string
	CostCents int64
	CostTime  time.Time
	Notes     string
	Values    map[string]string
}

// CostPaySummary 是按币种汇总的支出合计（今日/本月/今年）。
type CostPaySummary struct {
	Currency   string `json:"currency"`
	TodayCents int64  `json:"today_cents"`
	MonthCents int64  `json:"month_cents"`
	YearCents  int64  `json:"year_cents"`
}

const costPayFieldSelect = `SELECT public_id::text,field_name,field_type,is_required,field_option,show_list,sort_weight,created_at FROM cost_pay_fields`

func scanCostPayField(row pgx.Row) (CostPayField, error) {
	var v CostPayField
	err := row.Scan(&v.PublicID, &v.FieldName, &v.FieldType, &v.IsRequired, &v.FieldOption, &v.ShowList, &v.SortWeight, &v.CreatedAt)
	return v, err
}

// ListCostPayFields 列出全部自定义字段（按排序值）。
func (s *Store) ListCostPayFields(ctx context.Context) ([]CostPayField, error) {
	rows, err := s.DB.Query(ctx, costPayFieldSelect+` ORDER BY sort_weight,id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CostPayField{}
	for rows.Next() {
		v, err := scanCostPayField(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// CreateCostPayField 新增自定义字段，排到末尾。
func (s *Store) CreateCostPayField(ctx context.Context, name, ftype string, required bool, option string) (CostPayField, error) {
	var publicID string
	err := s.DB.QueryRow(ctx, `INSERT INTO cost_pay_fields(field_name,field_type,is_required,field_option,sort_weight)
VALUES($1,$2,$3,$4,(SELECT coalesce(max(sort_weight),0)+1 FROM cost_pay_fields)) RETURNING public_id::text`,
		name, ftype, required, option).Scan(&publicID)
	if err != nil {
		return CostPayField{}, err
	}
	return scanCostPayField(s.DB.QueryRow(ctx, costPayFieldSelect+` WHERE public_id=$1`, publicID))
}

// UpdateCostPayField 修改字段定义（不含排序与列表展示开关）。
func (s *Store) UpdateCostPayField(ctx context.Context, publicID, name, ftype string, required bool, option string) error {
	tag, err := s.DB.Exec(ctx, `UPDATE cost_pay_fields SET field_name=$2,field_type=$3,is_required=$4,field_option=$5,updated_at=now() WHERE public_id=$1`,
		publicID, name, ftype, required, option)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// SetCostPayFieldShowList 切换字段是否在支出列表展示。
func (s *Store) SetCostPayFieldShowList(ctx context.Context, publicID string, show bool) error {
	tag, err := s.DB.Exec(ctx, `UPDATE cost_pay_fields SET show_list=$2,updated_at=now() WHERE public_id=$1`, publicID, show)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// MoveCostPayField 把字段移到 prev 字段之后（prev 为空或 "0" 表示移到最前）。
// 实现按当前顺序重排后整表重写权重：字段数量少，结果稳定且无权重碰撞。
func (s *Store) MoveCostPayField(ctx context.Context, publicID, prevPublicID string) error {
	fields, err := s.ListCostPayFields(ctx)
	if err != nil {
		return err
	}
	ids := make([]string, 0, len(fields))
	var moving bool
	for _, f := range fields {
		if f.PublicID == publicID {
			moving = true
			continue
		}
		ids = append(ids, f.PublicID)
	}
	if !moving {
		return ErrNotFound
	}
	pos := 0
	if prevPublicID != "" && prevPublicID != "0" {
		pos = -1
		for i, id := range ids {
			if id == prevPublicID {
				pos = i + 1
				break
			}
		}
		if pos == -1 {
			return ErrNotFound
		}
	}
	ids = append(ids, "")
	copy(ids[pos+1:], ids[pos:])
	ids[pos] = publicID
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	for i, id := range ids {
		if _, err := tx.Exec(ctx, `UPDATE cost_pay_fields SET sort_weight=$2,updated_at=now() WHERE public_id=$1`, id, int64(i+1)); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// DeleteCostPayField 删除字段（其字段值随之删除）。
func (s *Store) DeleteCostPayField(ctx context.Context, publicID string) error {
	tag, err := s.DB.Exec(ctx, `DELETE FROM cost_pay_fields WHERE public_id=$1`, publicID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

const costPaySelect = `SELECT cp.public_id::text,o.public_id::text,cp.name,cp.owner,cp.cost_cents,cp.cost_time,cp.notes,coalesce(u.nickname,''),cp.created_at
FROM order_cost_pays cp JOIN orders o ON o.id=cp.order_id LEFT JOIN users u ON u.id=cp.admin_id`

// ListOrderCostPays 列出某订单的支出记录，返回记录、总数与主体候选。
func (s *Store) ListOrderCostPays(ctx context.Context, orderPublicID string, f CostPayFilter) ([]CostPay, int64, []string, error) {
	var orderID int64
	if err := s.DB.QueryRow(ctx, `SELECT id FROM orders WHERE public_id=$1`, orderPublicID).Scan(&orderID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, 0, nil, ErrNotFound
		}
		return nil, 0, nil, err
	}
	if f.Page < 1 {
		f.Page = 1
	}
	if f.Limit < 1 || f.Limit > 200 {
		f.Limit = 10
	}
	args := []any{orderID}
	where := []string{"cp.order_id=$1"}
	add := func(cond string, v any) {
		args = append(args, v)
		where = append(where, strings.Replace(cond, "?", fmt.Sprintf("$%d", len(args)), 1))
	}
	if f.Keywords != "" {
		args = append(args, "%"+f.Keywords+"%")
		n := fmt.Sprintf("$%d", len(args))
		where = append(where, "(cp.name ILIKE "+n+" OR cp.notes ILIKE "+n+")")
	}
	if f.Owner != "" {
		add("cp.owner=?", f.Owner)
	}
	if f.StartCostTime != nil {
		add("cp.cost_time>=?", *f.StartCostTime)
	}
	if f.EndCostTime != nil {
		add("cp.cost_time<=?", *f.EndCostTime)
	}
	if f.StartCreateTime != nil {
		add("cp.created_at>=?", *f.StartCreateTime)
	}
	if f.EndCreateTime != nil {
		add("cp.created_at<=?", *f.EndCreateTime)
	}
	cond := strings.Join(where, " AND ")
	var count int64
	if err := s.DB.QueryRow(ctx, `SELECT count(*) FROM order_cost_pays cp WHERE `+cond, args...).Scan(&count); err != nil {
		return nil, 0, nil, err
	}
	pageArgs := append(append([]any{}, args...), f.Limit, (f.Page-1)*f.Limit)
	q := costPaySelect + ` WHERE ` + cond + ` ORDER BY cp.cost_time DESC,cp.id DESC LIMIT $` + fmt.Sprintf("%d", len(args)+1) + ` OFFSET $` + fmt.Sprintf("%d", len(args)+2)
	rows, err := s.DB.Query(ctx, q, pageArgs...)
	if err != nil {
		return nil, 0, nil, err
	}
	list := []CostPay{}
	for rows.Next() {
		var v CostPay
		if err := rows.Scan(&v.PublicID, &v.OrderPublicID, &v.Name, &v.Owner, &v.CostCents, &v.CostTime, &v.Notes, &v.AdminName, &v.CreatedAt); err != nil {
			rows.Close()
			return nil, 0, nil, err
		}
		v.Values = map[string]string{}
		list = append(list, v)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, 0, nil, err
	}
	if err := s.attachCostPayValues(ctx, list); err != nil {
		return nil, 0, nil, err
	}
	owners := []string{}
	orows, err := s.DB.Query(ctx, `SELECT DISTINCT owner FROM order_cost_pays WHERE order_id=$1 AND owner<>'' ORDER BY owner`, orderID)
	if err != nil {
		return nil, 0, nil, err
	}
	defer orows.Close()
	for orows.Next() {
		var o string
		if err := orows.Scan(&o); err != nil {
			return nil, 0, nil, err
		}
		owners = append(owners, o)
	}
	return list, count, owners, orows.Err()
}

// attachCostPayValues 批量把字段值挂到支出记录上（键为字段 public_id）。
func (s *Store) attachCostPayValues(ctx context.Context, list []CostPay) error {
	if len(list) == 0 {
		return nil
	}
	ids := make([]string, len(list))
	index := map[string]int{}
	for i := range list {
		ids[i] = list[i].PublicID
		index[list[i].PublicID] = i
	}
	rows, err := s.DB.Query(ctx, `SELECT cp.public_id::text,f.public_id::text,v.value FROM order_cost_pay_values v
JOIN order_cost_pays cp ON cp.id=v.cost_pay_id JOIN cost_pay_fields f ON f.id=v.field_id
WHERE cp.public_id=ANY($1::uuid[])`, ids)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var costID, fieldID, value string
		if err := rows.Scan(&costID, &fieldID, &value); err != nil {
			return err
		}
		if i, ok := index[costID]; ok {
			list[i].Values[fieldID] = value
		}
	}
	return rows.Err()
}

// GetOrderCostPay 读取一条支出记录（含字段值）。
func (s *Store) GetOrderCostPay(ctx context.Context, publicID string) (CostPay, error) {
	var v CostPay
	err := s.DB.QueryRow(ctx, costPaySelect+` WHERE cp.public_id=$1`, publicID).
		Scan(&v.PublicID, &v.OrderPublicID, &v.Name, &v.Owner, &v.CostCents, &v.CostTime, &v.Notes, &v.AdminName, &v.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return CostPay{}, ErrNotFound
	}
	if err != nil {
		return CostPay{}, err
	}
	v.Values = map[string]string{}
	list := []CostPay{v}
	if err := s.attachCostPayValues(ctx, list); err != nil {
		return CostPay{}, err
	}
	return list[0], nil
}

// validateCostPayValues 校验必填与下拉取值，并返回清洗后的字段值。
func (s *Store) validateCostPayValues(ctx context.Context, values map[string]string) (map[string]string, error) {
	fields, err := s.ListCostPayFields(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, f := range fields {
		raw := strings.TrimSpace(values[f.PublicID])
		if raw == "" {
			if f.IsRequired {
				return nil, errors.New("自定义字段「" + f.FieldName + "」为必填")
			}
			continue
		}
		if f.FieldType == "dropdown" {
			ok := false
			for _, opt := range strings.Split(f.FieldOption, ",") {
				if strings.TrimSpace(opt) == raw {
					ok = true
					break
				}
			}
			if !ok {
				return nil, errors.New("自定义字段「" + f.FieldName + "」取值不在下拉选项中")
			}
		}
		out[f.PublicID] = raw
	}
	return out, nil
}

func (s *Store) upsertCostPayValues(ctx context.Context, tx pgx.Tx, costPayID int64, values map[string]string) error {
	if _, err := tx.Exec(ctx, `DELETE FROM order_cost_pay_values WHERE cost_pay_id=$1`, costPayID); err != nil {
		return err
	}
	for fieldPublicID, value := range values {
		if _, err := tx.Exec(ctx, `INSERT INTO order_cost_pay_values(cost_pay_id,field_id,value)
SELECT $1,id,$3 FROM cost_pay_fields WHERE public_id=$2`, costPayID, fieldPublicID, value); err != nil {
			return err
		}
	}
	return nil
}

// CreateOrderCostPay 在某订单下新增一条支出。
func (s *Store) CreateOrderCostPay(ctx context.Context, orderPublicID string, adminID int64, in CostPayInput) (CostPay, error) {
	values, err := s.validateCostPayValues(ctx, in.Values)
	if err != nil {
		return CostPay{}, err
	}
	var orderID int64
	if err := s.DB.QueryRow(ctx, `SELECT id FROM orders WHERE public_id=$1`, orderPublicID).Scan(&orderID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return CostPay{}, ErrNotFound
		}
		return CostPay{}, err
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return CostPay{}, err
	}
	defer tx.Rollback(ctx)
	var costPayID int64
	var publicID string
	if err := tx.QueryRow(ctx, `INSERT INTO order_cost_pays(order_id,name,owner,cost_cents,cost_time,notes,admin_id)
VALUES($1,$2,$3,$4,$5,$6,nullif($7,0)::bigint) RETURNING id,public_id::text`,
		orderID, in.Name, in.Owner, in.CostCents, in.CostTime, in.Notes, adminID).Scan(&costPayID, &publicID); err != nil {
		return CostPay{}, err
	}
	if err := s.upsertCostPayValues(ctx, tx, costPayID, values); err != nil {
		return CostPay{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return CostPay{}, err
	}
	return s.GetOrderCostPay(ctx, publicID)
}

// UpdateOrderCostPay 修改一条支出。
func (s *Store) UpdateOrderCostPay(ctx context.Context, publicID string, in CostPayInput) error {
	values, err := s.validateCostPayValues(ctx, in.Values)
	if err != nil {
		return err
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var costPayID int64
	if err := tx.QueryRow(ctx, `UPDATE order_cost_pays SET name=$2,owner=$3,cost_cents=$4,cost_time=$5,notes=$6,updated_at=now()
WHERE public_id=$1 RETURNING id`, publicID, in.Name, in.Owner, in.CostCents, in.CostTime, in.Notes).Scan(&costPayID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if err := s.upsertCostPayValues(ctx, tx, costPayID, values); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// DeleteOrderCostPay 删除一条支出。
func (s *Store) DeleteOrderCostPay(ctx context.Context, publicID string) error {
	tag, err := s.DB.Exec(ctx, `DELETE FROM order_cost_pays WHERE public_id=$1`, publicID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// CostPaySummary 按币种汇总今日/本月/今年的支出合计（对应插件看板 widget）。
func (s *Store) CostPaySummary(ctx context.Context) ([]CostPaySummary, error) {
	rows, err := s.DB.Query(ctx, `SELECT o.currency,
coalesce(sum(cp.cost_cents) FILTER (WHERE cp.cost_time>=date_trunc('day',now())),0),
coalesce(sum(cp.cost_cents) FILTER (WHERE cp.cost_time>=date_trunc('month',now())),0),
coalesce(sum(cp.cost_cents) FILTER (WHERE cp.cost_time>=date_trunc('year',now())),0)
FROM order_cost_pays cp JOIN orders o ON o.id=cp.order_id GROUP BY o.currency ORDER BY o.currency`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CostPaySummary{}
	for rows.Next() {
		var v CostPaySummary
		if err := rows.Scan(&v.Currency, &v.TodayCents, &v.MonthCents, &v.YearCents); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// OrderBrief 是支出页面顶部展示的订单摘要。
type OrderBrief struct {
	PublicID   string    `json:"id"`
	Status     string    `json:"status"`
	TotalCents int64     `json:"total_cents"`
	Currency   string    `json:"currency"`
	CreatedAt  time.Time `json:"created_at"`
}

// GetOrderBrief 读取订单摘要（不存在返回 ErrNotFound）。
func (s *Store) GetOrderBrief(ctx context.Context, publicID string) (OrderBrief, error) {
	var v OrderBrief
	err := s.DB.QueryRow(ctx, `SELECT public_id::text,status,total_cents,currency,created_at FROM orders WHERE public_id=$1`, publicID).
		Scan(&v.PublicID, &v.Status, &v.TotalCents, &v.Currency, &v.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return OrderBrief{}, ErrNotFound
	}
	if err != nil {
		return OrderBrief{}, err
	}
	return v, nil
}
