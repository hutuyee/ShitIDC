package store

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// 流量包（对齐魔方 CBAP FlowPacket 插件）。
//
// 后台维护流量包：名称、流量（GB）、售价、可用库存（开关控制）、关联商品（多选）。
// 用户端只能给「名下 + 关联商品匹配 + 未删除」的产品购买，余额支付；
// 订单状态与插件一致：unpaid / paid / cancelled / refunded。
//
// 说明：本站的服务是接口无关的资源，流量到账依赖上游能力，订单付款后由
// 事件总线广播（kind=flow_packet_paid），不在本文件里直接改服务。

var (
	// ErrFlowPacketInactive 流量包已下架。
	ErrFlowPacketInactive = errors.New("flow packet is inactive")
	// ErrFlowPacketSoldOut 流量包库存不足。
	ErrFlowPacketSoldOut = errors.New("flow packet is sold out")
	// ErrFlowPacketProduct 关联商品无效（不存在或为空）。
	ErrFlowPacketProduct = errors.New("invalid associated product")
	// ErrFlowPacketIneligible 产品与流量包不匹配。
	ErrFlowPacketIneligible = errors.New("service is not eligible for this flow packet")
)

// FlowPacketProduct 是流量包关联的商品。
type FlowPacketProduct struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// FlowPacketService 是用户可以为某个流量包选择的产品（用户端列表用）。
type FlowPacketService struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"`
}

// FlowPacket 是一个流量包配置。
type FlowPacket struct {
	ID          int64               `json:"id"`
	PublicID    string              `json:"public_id"`
	Name        string              `json:"name"`
	CapacityGB  int                 `json:"capacity_gb"`
	PriceCents  int64               `json:"price_cents"`
	Stock       int                 `json:"stock"`
	StockEnable bool                `json:"stock_enable"`
	Notes       string              `json:"notes"`
	Active      bool                `json:"active"`
	Products    []FlowPacketProduct `json:"products"`
	Services    []FlowPacketService `json:"services,omitempty"`
	CreatedAt   time.Time           `json:"created_at"`
	UpdatedAt   time.Time           `json:"updated_at"`
}

// FlowPacketOrder 是一条流量包购买订单。
type FlowPacketOrder struct {
	ID          int64      `json:"id"`
	PublicID    string     `json:"public_id"`
	UserID      int64      `json:"user_uid"`
	Username    string     `json:"username"`
	PacketID    string     `json:"packet_id"`
	PacketName  string     `json:"packet_name"`
	CapacityGB  int        `json:"capacity_gb"`
	ServiceID   string     `json:"service_id"`
	ServiceName string     `json:"service_name"`
	AmountCents int64      `json:"amount_cents"`
	Status      string     `json:"status"`
	CreatedAt   time.Time  `json:"created_at"`
	PaidAt      *time.Time `json:"paid_at"`
}

// FlowPacketInput 是新增 / 修改流量包的入参（商品为公开 ID）。
type FlowPacketInput struct {
	Name        string
	CapacityGB  int
	PriceCents  int64
	Stock       int
	StockEnable bool
	Notes       string
	ProductIDs  []string
}

const flowPacketCols = `SELECT fp.id,fp.public_id::text,fp.name,fp.capacity_gb,fp.price_cents,fp.stock,fp.stock_enable,fp.notes,fp.active,fp.created_at,fp.updated_at`

const flowPacketOrderSelect = `SELECT fpo.id,fpo.public_id::text,fpo.user_id,u.email,COALESCE(fp.public_id::text,''),fpo.packet_name,fpo.capacity_gb,COALESCE(sv.public_id::text,''),COALESCE(pr.name,fpo.service_name),fpo.amount_cents,fpo.status,fpo.created_at,fpo.paid_at
FROM flow_packet_orders fpo
JOIN users u ON u.id=fpo.user_id
LEFT JOIN flow_packets fp ON fp.id=fpo.packet_id
LEFT JOIN services sv ON sv.id=fpo.service_id
LEFT JOIN products pr ON pr.id=sv.product_id`

func scanFlowPacketOrder(row pgx.Row) (FlowPacketOrder, error) {
	var v FlowPacketOrder
	err := row.Scan(&v.ID, &v.PublicID, &v.UserID, &v.Username, &v.PacketID, &v.PacketName, &v.CapacityGB, &v.ServiceID, &v.ServiceName, &v.AmountCents, &v.Status, &v.CreatedAt, &v.PaidAt)
	return v, err
}

func (s *Store) getFlowPacketOrder(ctx context.Context, publicID string, userID int64) (FlowPacketOrder, error) {
	v, err := scanFlowPacketOrder(s.DB.QueryRow(ctx, flowPacketOrderSelect+` WHERE fpo.public_id=$1 AND fpo.user_id=$2`, publicID, userID))
	if errors.Is(err, pgx.ErrNoRows) {
		return FlowPacketOrder{}, ErrNotFound
	}
	return v, err
}

// attachFlowPacketProducts 给一批流量包挂上关联商品（一次查询）。
func (s *Store) attachFlowPacketProducts(ctx context.Context, out []FlowPacket, ids []int64) error {
	for i := range out {
		out[i].Products = []FlowPacketProduct{}
	}
	if len(ids) == 0 {
		return nil
	}
	rows, err := s.DB.Query(ctx, `SELECT fpp.packet_id,pr.public_id::text,pr.name
FROM flow_packet_products fpp JOIN products pr ON pr.id=fpp.product_id
WHERE fpp.packet_id=ANY($1::bigint[]) ORDER BY pr.name`, ids)
	if err != nil {
		return err
	}
	defer rows.Close()
	pos := make(map[int64]int, len(out))
	for i := range out {
		pos[out[i].ID] = i
	}
	for rows.Next() {
		var packetID int64
		var publicID, name string
		if err := rows.Scan(&packetID, &publicID, &name); err != nil {
			return err
		}
		if i, ok := pos[packetID]; ok {
			out[i].Products = append(out[i].Products, FlowPacketProduct{ID: publicID, Name: name})
		}
	}
	return rows.Err()
}

func (s *Store) getFlowPacket(ctx context.Context, publicID string) (FlowPacket, error) {
	v, err := scanFlowPacket(s.DB.QueryRow(ctx, flowPacketCols+` FROM flow_packets fp WHERE fp.public_id=$1`, publicID))
	if errors.Is(err, pgx.ErrNoRows) {
		return FlowPacket{}, ErrNotFound
	}
	if err != nil {
		return FlowPacket{}, err
	}
	out := []FlowPacket{v}
	if err := s.attachFlowPacketProducts(ctx, out, []int64{v.ID}); err != nil {
		return FlowPacket{}, err
	}
	return out[0], nil
}

func scanFlowPacket(row pgx.Row) (FlowPacket, error) {
	var v FlowPacket
	err := row.Scan(&v.ID, &v.PublicID, &v.Name, &v.CapacityGB, &v.PriceCents, &v.Stock, &v.StockEnable, &v.Notes, &v.Active, &v.CreatedAt, &v.UpdatedAt)
	return v, err
}

// ListFlowPackets 后台分页列出流量包；active 为 nil 表示不过滤状态。
func (s *Store) ListFlowPackets(ctx context.Context, keyword string, active *bool, limit, offset int) ([]FlowPacket, int64, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	if offset < 0 {
		offset = 0
	}
	keyword = strings.TrimSpace(keyword)
	var activeArg any
	if active != nil {
		activeArg = *active
	}
	where := ` FROM flow_packets fp
WHERE ($1='' OR fp.name ILIKE '%'||$1||'%' OR fp.notes ILIKE '%'||$1||'%')
  AND ($2::boolean IS NULL OR fp.active=$2)`
	rows, err := s.DB.Query(ctx, flowPacketCols+where+` ORDER BY fp.id DESC LIMIT $3 OFFSET $4`, keyword, activeArg, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []FlowPacket{}
	ids := []int64{}
	for rows.Next() {
		v, err := scanFlowPacket(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, v)
		ids = append(ids, v.ID)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	if err := s.attachFlowPacketProducts(ctx, out, ids); err != nil {
		return nil, 0, err
	}
	var total int64
	if err := s.DB.QueryRow(ctx, `SELECT count(*)`+where, keyword, activeArg).Scan(&total); err != nil {
		return nil, 0, err
	}
	return out, total, nil
}

// resolveFlowPacketProductIDs 把商品公开 ID 解析成内部 ID；空列表或有不存在的商品时报错。
func resolveFlowPacketProductIDs(ctx context.Context, tx pgx.Tx, publicIDs []string) ([]int64, error) {
	seen := map[string]bool{}
	ids := []int64{}
	for _, pid := range publicIDs {
		pid = strings.TrimSpace(pid)
		if pid == "" || seen[pid] {
			continue
		}
		seen[pid] = true
		var id int64
		err := tx.QueryRow(ctx, `SELECT id FROM products WHERE public_id=$1 AND deleted_at IS NULL`, pid).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrFlowPacketProduct
		}
		if err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return nil, ErrFlowPacketProduct
	}
	return ids, nil
}

// CreateFlowPacket 新增流量包。
func (s *Store) CreateFlowPacket(ctx context.Context, in FlowPacketInput) (FlowPacket, error) {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return FlowPacket{}, err
	}
	defer tx.Rollback(ctx)
	productIDs, err := resolveFlowPacketProductIDs(ctx, tx, in.ProductIDs)
	if err != nil {
		return FlowPacket{}, err
	}
	var id int64
	var publicID string
	err = tx.QueryRow(ctx, `INSERT INTO flow_packets(name,capacity_gb,price_cents,stock,stock_enable,notes,active)
VALUES($1,$2,$3,$4,$5,$6,true) RETURNING id,public_id::text`,
		strings.TrimSpace(in.Name), in.CapacityGB, in.PriceCents, in.Stock, in.StockEnable, strings.TrimSpace(in.Notes)).Scan(&id, &publicID)
	if err != nil {
		return FlowPacket{}, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO flow_packet_products(packet_id,product_id) SELECT $1,unnest($2::bigint[])`, id, productIDs); err != nil {
		return FlowPacket{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return FlowPacket{}, err
	}
	return s.getFlowPacket(ctx, publicID)
}

// UpdateFlowPacket 修改流量包（含关联商品整体替换）。
func (s *Store) UpdateFlowPacket(ctx context.Context, publicID string, in FlowPacketInput) (FlowPacket, error) {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return FlowPacket{}, err
	}
	defer tx.Rollback(ctx)
	var id int64
	err = tx.QueryRow(ctx, `SELECT id FROM flow_packets WHERE public_id=$1 FOR UPDATE`, publicID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return FlowPacket{}, ErrNotFound
	}
	if err != nil {
		return FlowPacket{}, err
	}
	productIDs, err := resolveFlowPacketProductIDs(ctx, tx, in.ProductIDs)
	if err != nil {
		return FlowPacket{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE flow_packets SET name=$2,capacity_gb=$3,price_cents=$4,stock=$5,stock_enable=$6,notes=$7,updated_at=now() WHERE id=$1`,
		id, strings.TrimSpace(in.Name), in.CapacityGB, in.PriceCents, in.Stock, in.StockEnable, strings.TrimSpace(in.Notes)); err != nil {
		return FlowPacket{}, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM flow_packet_products WHERE packet_id=$1`, id); err != nil {
		return FlowPacket{}, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO flow_packet_products(packet_id,product_id) SELECT $1,unnest($2::bigint[])`, id, productIDs); err != nil {
		return FlowPacket{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return FlowPacket{}, err
	}
	return s.getFlowPacket(ctx, publicID)
}

// SetFlowPacketActive 启停流量包。
func (s *Store) SetFlowPacketActive(ctx context.Context, publicID string, active bool) error {
	tag, err := s.DB.Exec(ctx, `UPDATE flow_packets SET active=$2,updated_at=now() WHERE public_id=$1`, publicID, active)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteFlowPacket 删除流量包；历史订单保留名称快照。
func (s *Store) DeleteFlowPacket(ctx context.Context, publicID string) error {
	tag, err := s.DB.Exec(ctx, `DELETE FROM flow_packets WHERE public_id=$1`, publicID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ListFlowPacketOrders 后台按关键词 / 状态分页读取流量包订单。
func (s *Store) ListFlowPacketOrders(ctx context.Context, keyword, status string, limit, offset int) ([]FlowPacketOrder, int64, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	if offset < 0 {
		offset = 0
	}
	keyword = strings.TrimSpace(keyword)
	where := ` WHERE ($1='' OR u.email ILIKE '%'||$1||'%' OR fpo.packet_name ILIKE '%'||$1||'%'
       OR fpo.service_name ILIKE '%'||$1||'%' OR COALESCE(sv.public_id::text,'') ILIKE '%'||$1||'%')
  AND ($2='' OR fpo.status=$2)`
	rows, err := s.DB.Query(ctx, flowPacketOrderSelect+where+` ORDER BY fpo.id DESC LIMIT $3 OFFSET $4`, keyword, status, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []FlowPacketOrder{}
	for rows.Next() {
		v, err := scanFlowPacketOrder(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	var total int64
	if err := s.DB.QueryRow(ctx, `SELECT count(*)
FROM flow_packet_orders fpo
JOIN users u ON u.id=fpo.user_id
LEFT JOIN services sv ON sv.id=fpo.service_id`+where, keyword, status).Scan(&total); err != nil {
		return nil, 0, err
	}
	return out, total, nil
}

// ListUserFlowPacketOrders 用户读取自己的流量包订单。
func (s *Store) ListUserFlowPacketOrders(ctx context.Context, userID int64) ([]FlowPacketOrder, error) {
	rows, err := s.DB.Query(ctx, flowPacketOrderSelect+` WHERE fpo.user_id=$1 ORDER BY fpo.id DESC LIMIT 200`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []FlowPacketOrder{}
	for rows.Next() {
		v, err := scanFlowPacketOrder(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// DeleteFlowPacketOrder 后台删除订单记录。
func (s *Store) DeleteFlowPacketOrder(ctx context.Context, publicID string) error {
	tag, err := s.DB.Exec(ctx, `DELETE FROM flow_packet_orders WHERE public_id=$1`, publicID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ListUserFlowPackets 用户端列出上架中的流量包，并给出每个包可用于哪些名下产品。
func (s *Store) ListUserFlowPackets(ctx context.Context, userID int64) ([]FlowPacket, error) {
	rows, err := s.DB.Query(ctx, flowPacketCols+`
FROM flow_packets fp
LEFT JOIN flow_packet_products fpp ON fpp.packet_id=fp.id
LEFT JOIN products pr ON pr.id=fpp.product_id
WHERE fp.active=true
ORDER BY fp.id DESC, pr.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	packets := []FlowPacket{}
	index := map[int64]int{}
	linked := map[int64][]int64{}
	for rows.Next() {
		var v FlowPacket
		var productID *int64
		var productPublic, productName *string
		if err := rows.Scan(&v.ID, &v.PublicID, &v.Name, &v.CapacityGB, &v.PriceCents, &v.Stock, &v.StockEnable, &v.Notes, &v.Active, &v.CreatedAt, &v.UpdatedAt,
			&productID, &productPublic, &productName); err != nil {
			return nil, err
		}
		idx, ok := index[v.ID]
		if !ok {
			v.Products = []FlowPacketProduct{}
			v.Services = []FlowPacketService{}
			packets = append(packets, v)
			idx = len(packets) - 1
			index[v.ID] = idx
		}
		if productID != nil && productPublic != nil && productName != nil {
			packets[idx].Products = append(packets[idx].Products, FlowPacketProduct{ID: *productPublic, Name: *productName})
			linked[v.ID] = append(linked[v.ID], *productID)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	type userService struct {
		ID         string
		Name       string
		Status     string
		ProductRef int64
	}
	services := []userService{}
	rows2, err := s.DB.Query(ctx, `SELECT sv.public_id::text,p.name,sv.status,sv.product_id
FROM services sv JOIN products p ON p.id=sv.product_id
WHERE sv.user_id=$1 AND sv.status IN ('active','suspending','suspended')
ORDER BY sv.created_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows2.Close()
	for rows2.Next() {
		var v userService
		if err := rows2.Scan(&v.ID, &v.Name, &v.Status, &v.ProductRef); err != nil {
			return nil, err
		}
		services = append(services, v)
	}
	if err := rows2.Err(); err != nil {
		return nil, err
	}
	for i := range packets {
		ids := linked[packets[i].ID]
		for _, sv := range services {
			for _, pid := range ids {
				if pid == sv.ProductRef {
					packets[i].Services = append(packets[i].Services, FlowPacketService{ID: sv.ID, Name: sv.Name, Status: sv.Status})
					break
				}
			}
		}
	}
	return packets, nil
}

// CreateFlowPacketOrder 为用户创建一个待付款流量包订单（供余额支付）。
func (s *Store) CreateFlowPacketOrder(ctx context.Context, userID int64, packetPublicID, servicePublicID string) (FlowPacketOrder, error) {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return FlowPacketOrder{}, err
	}
	defer tx.Rollback(ctx)

	var packetID int64
	var packetName string
	var capacityGB, stock int
	var priceCents int64
	var stockEnable, active bool
	err = tx.QueryRow(ctx, `SELECT id,name,capacity_gb,price_cents,stock,stock_enable,active FROM flow_packets WHERE public_id=$1 FOR UPDATE`, packetPublicID).
		Scan(&packetID, &packetName, &capacityGB, &priceCents, &stock, &stockEnable, &active)
	if errors.Is(err, pgx.ErrNoRows) {
		return FlowPacketOrder{}, ErrNotFound
	}
	if err != nil {
		return FlowPacketOrder{}, err
	}
	if !active {
		return FlowPacketOrder{}, ErrFlowPacketInactive
	}
	if stockEnable && stock <= 0 {
		return FlowPacketOrder{}, ErrFlowPacketSoldOut
	}

	var serviceID, productID int64
	var serviceName, serviceStatus string
	err = tx.QueryRow(ctx, `SELECT sv.id,sv.product_id,p.name,sv.status FROM services sv JOIN products p ON p.id=sv.product_id WHERE sv.public_id=$1 AND sv.user_id=$2`, servicePublicID, userID).
		Scan(&serviceID, &productID, &serviceName, &serviceStatus)
	if errors.Is(err, pgx.ErrNoRows) {
		return FlowPacketOrder{}, ErrNotFound
	}
	if err != nil {
		return FlowPacketOrder{}, err
	}
	if serviceStatus != "active" && serviceStatus != "suspended" && serviceStatus != "suspending" {
		return FlowPacketOrder{}, ErrInvalidState
	}
	var linked bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM flow_packet_products WHERE packet_id=$1 AND product_id=$2)`, packetID, productID).Scan(&linked); err != nil {
		return FlowPacketOrder{}, err
	}
	if !linked {
		return FlowPacketOrder{}, ErrFlowPacketIneligible
	}

	var publicID string
	err = tx.QueryRow(ctx, `INSERT INTO flow_packet_orders(user_id,packet_id,packet_name,capacity_gb,service_id,service_name,amount_cents,status)
VALUES($1,$2,$3,$4,$5,$6,$7,'unpaid') RETURNING public_id::text`,
		userID, packetID, packetName, capacityGB, serviceID, serviceName, priceCents).Scan(&publicID)
	if err != nil {
		return FlowPacketOrder{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return FlowPacketOrder{}, err
	}
	return s.getFlowPacketOrder(ctx, publicID, userID)
}

// PayFlowPacketOrder 用余额支付流量包订单；余额不足返回 ErrInsufficientBalance。
func (s *Store) PayFlowPacketOrder(ctx context.Context, userID int64, orderPublicID, idempotency string) (FlowPacketOrder, error) {
	if idempotency == "" {
		idempotency = uuid.NewString()
	}
	var out FlowPacketOrder
	err := retrySerializable(ctx, orderRetryAttempts, func() error {
		res, err := s.payFlowPacketOrderOnce(ctx, userID, orderPublicID, idempotency)
		if err != nil {
			return err
		}
		out = res
		return nil
	})
	return out, err
}

func (s *Store) payFlowPacketOrderOnce(ctx context.Context, userID int64, orderPublicID, idempotency string) (FlowPacketOrder, error) {
	tx, err := s.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return FlowPacketOrder{}, err
	}
	defer tx.Rollback(ctx)

	var id, amount int64
	var status string
	var packetID *int64
	err = tx.QueryRow(ctx, `SELECT id,amount_cents,status,packet_id FROM flow_packet_orders WHERE public_id=$1 AND user_id=$2 FOR UPDATE`, orderPublicID, userID).
		Scan(&id, &amount, &status, &packetID)
	if errors.Is(err, pgx.ErrNoRows) {
		return FlowPacketOrder{}, ErrNotFound
	}
	if err != nil {
		return FlowPacketOrder{}, err
	}
	if status == "paid" {
		if err := tx.Commit(ctx); err != nil {
			return FlowPacketOrder{}, err
		}
		return s.getFlowPacketOrder(ctx, orderPublicID, userID)
	}
	if status != "unpaid" {
		return FlowPacketOrder{}, ErrInvalidState
	}

	if packetID != nil {
		var stock int
		var stockEnable bool
		err = tx.QueryRow(ctx, `SELECT stock,stock_enable FROM flow_packets WHERE id=$1 FOR UPDATE`, *packetID).Scan(&stock, &stockEnable)
		if err == nil && stockEnable {
			if stock <= 0 {
				return FlowPacketOrder{}, ErrFlowPacketSoldOut
			}
			if _, err := tx.Exec(ctx, `UPDATE flow_packets SET stock=stock-1,updated_at=now() WHERE id=$1`, *packetID); err != nil {
				return FlowPacketOrder{}, err
			}
		} else if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return FlowPacketOrder{}, err
		}
	}
	if err := debitWalletTx(ctx, tx, userID, "CNY", amount, "flow_packet", orderPublicID, idempotency); err != nil {
		return FlowPacketOrder{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE flow_packet_orders SET status='paid',paid_at=now() WHERE id=$1`, id); err != nil {
		return FlowPacketOrder{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return FlowPacketOrder{}, err
	}
	return s.getFlowPacketOrder(ctx, orderPublicID, userID)
}

// CancelFlowPacketOrder 取消未付款订单。
func (s *Store) CancelFlowPacketOrder(ctx context.Context, userID int64, orderPublicID string) (FlowPacketOrder, error) {
	tag, err := s.DB.Exec(ctx, `UPDATE flow_packet_orders SET status='cancelled' WHERE public_id=$1 AND user_id=$2 AND status='unpaid'`, orderPublicID, userID)
	if err != nil {
		return FlowPacketOrder{}, err
	}
	if tag.RowsAffected() == 0 {
		var status string
		err := s.DB.QueryRow(ctx, `SELECT status FROM flow_packet_orders WHERE public_id=$1 AND user_id=$2`, orderPublicID, userID).Scan(&status)
		if errors.Is(err, pgx.ErrNoRows) {
			return FlowPacketOrder{}, ErrNotFound
		}
		if err != nil {
			return FlowPacketOrder{}, err
		}
		return FlowPacketOrder{}, ErrInvalidState
	}
	return s.getFlowPacketOrder(ctx, orderPublicID, userID)
}
