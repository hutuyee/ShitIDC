package store

import (
	"context"
	"errors"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/hutuyee/ShitIDC/internal/ipmi"
)

// 手动资源（对齐魔方 CBAP ManualResource 插件）。
//
// 独立服务器台账：供应商 + 资源（主 IP / 附加 IP / 配置 / 成本 / 系统账密 /
// 控制方式）+ 分配（关联客户与产品）+ 电源操作。
// 控制方式 ipmi 走 internal/ipmi（IPMI 2.0 / RMCP+，纯标准库）；
// 控制方式 client（万云 DCIM 客户端）的协议在参考源里 ionCube 加密不可读，
// 电源操作明确报「不支持」——与插件语言包里对无控制方式资源的「不支持」一致。

// ErrManualUnsupported 是当前控制方式不支持该操作。
var ErrManualUnsupported = errors.New("manual resource: operation unsupported for this control mode")

// ErrManualInUse 是资源已分配仍要求空闲/重复分配等状态冲突。
var ErrManualInUse = errors.New("manual resource: state conflict")

// ManualSupplier 是一个上游供应商。
type ManualSupplier struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Contact   string    `json:"contact"`
	Notes     string    `json:"notes"`
	Count     int64     `json:"count"`
	CreatedAt time.Time `json:"created_at"`
}

// ManualSupplierInput 是供应商入参。
type ManualSupplierInput struct {
	Name    string
	Contact string
	Notes   string
}

const manualSupplierCols = `SELECT s.id,s.public_id::text,s.name,s.contact,s.notes,
(SELECT count(*) FROM manual_resources r WHERE r.supplier_id=s.id),s.created_at
FROM manual_suppliers s`

// ListManualSuppliers 列出供应商（含名下资源数）。
func (s *Store) ListManualSuppliers(ctx context.Context) ([]ManualSupplier, error) {
	rows, err := s.DB.Query(ctx, manualSupplierCols+` ORDER BY s.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ManualSupplier{}
	for rows.Next() {
		var v ManualSupplier
		if err := rows.Scan(new(int64), &v.ID, &v.Name, &v.Contact, &v.Notes, &v.Count, &v.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// CreateManualSupplier 新增供应商。
func (s *Store) CreateManualSupplier(ctx context.Context, in ManualSupplierInput) (ManualSupplier, error) {
	if strings.TrimSpace(in.Name) == "" {
		return ManualSupplier{}, ErrInvalidState
	}
	var publicID string
	err := s.DB.QueryRow(ctx, `INSERT INTO manual_suppliers(name,contact,notes) VALUES($1,$2,$3) RETURNING public_id::text`,
		strings.TrimSpace(in.Name), strings.TrimSpace(in.Contact), strings.TrimSpace(in.Notes)).Scan(&publicID)
	if err != nil {
		return ManualSupplier{}, err
	}
	return s.getManualSupplier(ctx, publicID)
}

func (s *Store) getManualSupplier(ctx context.Context, publicID string) (ManualSupplier, error) {
	var v ManualSupplier
	err := s.DB.QueryRow(ctx, manualSupplierCols+` WHERE s.public_id=$1`, publicID).
		Scan(new(int64), &v.ID, &v.Name, &v.Contact, &v.Notes, &v.Count, &v.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return ManualSupplier{}, ErrNotFound
	}
	return v, err
}

// UpdateManualSupplier 修改供应商。
func (s *Store) UpdateManualSupplier(ctx context.Context, publicID string, in ManualSupplierInput) (ManualSupplier, error) {
	tag, err := s.DB.Exec(ctx, `UPDATE manual_suppliers SET name=$2,contact=$3,notes=$4,updated_at=now() WHERE public_id=$1`,
		publicID, strings.TrimSpace(in.Name), strings.TrimSpace(in.Contact), strings.TrimSpace(in.Notes))
	if err != nil {
		return ManualSupplier{}, err
	}
	if tag.RowsAffected() == 0 {
		return ManualSupplier{}, ErrNotFound
	}
	return s.getManualSupplier(ctx, publicID)
}

// DeleteManualSupplier 删除供应商（名下资源的供应商字段置空）。
func (s *Store) DeleteManualSupplier(ctx context.Context, publicID string) error {
	tag, err := s.DB.Exec(ctx, `DELETE FROM manual_suppliers WHERE public_id=$1`, publicID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ManualResource 是一台手动资源（独立服务器）。
type ManualResource struct {
	ID              string     `json:"id"`
	DedicatedIP     string     `json:"dedicated_ip"`
	AssignedIPs     []string   `json:"assigned_ips"`
	Notes           string     `json:"notes"`
	Configuration   string     `json:"configuration"`
	CostCents       int64      `json:"cost_cents"`
	Username        string     `json:"username"`
	Password        string     `json:"password"`
	ControlMode     string     `json:"control_mode"`
	IpmiIP          string     `json:"ipmi_ip"`
	IpmiPort        int        `json:"ipmi_port"`
	IpmiVersion     string     `json:"ipmi_version"`
	DcimClientURL   string     `json:"dcim_client_url"`
	DcimClientID    string     `json:"dcim_client_id"`
	ControlUsername string     `json:"control_username"`
	ControlPassword string     `json:"control_password"`
	DueTime         *time.Time `json:"due_time"`
	SupplierID      string     `json:"supplier_id"`
	SupplierName    string     `json:"supplier_name"`
	UserID          int64      `json:"user_uid"`
	UserEmail       string     `json:"user_email"`
	ServiceID       string     `json:"service_id"`
	ServiceName     string     `json:"service_name"`
	Status          string     `json:"status"`
	PowerStatus     string     `json:"power_status"`
	CreatedAt       time.Time  `json:"created_at"`
}

// ManualResourceInput 是资源入参。
type ManualResourceInput struct {
	DedicatedIP     string
	AssignedIPs     string
	Notes           string
	Configuration   string
	CostCents       int64
	Username        string
	Password        string
	ControlMode     string
	IpmiIP          string
	IpmiPort        int
	IpmiVersion     string
	DcimClientURL   string
	DcimClientID    string
	ControlUsername string
	ControlPassword string
	DueTime         *time.Time
	SupplierID      string
}

const manualResourceCols = `SELECT r.id,r.public_id::text,r.dedicated_ip,r.assigned_ips,r.notes,r.configuration,r.cost_cents,
r.username,r.password,r.control_mode,r.ipmi_ip,r.ipmi_port,r.ipmi_version,r.dcim_client_url,r.dcim_client_id,
r.control_username,r.control_password,r.due_time,
coalesce(sp.public_id::text,''),coalesce(sp.name,''),
coalesce(u.uid,0),coalesce(u.email,''),
coalesce(sv.public_id::text,''),coalesce(pr.name,''),
r.status,r.power_status,r.created_at
FROM manual_resources r
LEFT JOIN manual_suppliers sp ON sp.id=r.supplier_id
LEFT JOIN users u ON u.id=r.user_id
LEFT JOIN services sv ON sv.id=r.service_id
LEFT JOIN products pr ON pr.id=sv.product_id`

func scanManualResource(row pgx.Row) (ManualResource, int64, error) {
	var v ManualResource
	var id int64
	var assignedIPs string
	err := row.Scan(&id, &v.ID, &v.DedicatedIP, &assignedIPs, &v.Notes, &v.Configuration, &v.CostCents,
		&v.Username, &v.Password, &v.ControlMode, &v.IpmiIP, &v.IpmiPort, &v.IpmiVersion, &v.DcimClientURL, &v.DcimClientID,
		&v.ControlUsername, &v.ControlPassword, &v.DueTime,
		&v.SupplierID, &v.SupplierName,
		&v.UserID, &v.UserEmail,
		&v.ServiceID, &v.ServiceName,
		&v.Status, &v.PowerStatus, &v.CreatedAt)
	if err == nil {
		v.AssignedIPs = []string{}
		for _, line := range strings.Split(assignedIPs, "\n") {
			if line = strings.TrimSpace(line); line != "" {
				v.AssignedIPs = append(v.AssignedIPs, line)
			}
		}
	}
	return v, id, err
}

// ListManualResources 分页列出手动资源（关键词匹配 IP / 配置 / 备注，与插件一致）。
func (s *Store) ListManualResources(ctx context.Context, keyword, supplierID, status string, limit, offset int) ([]ManualResource, int64, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	if offset < 0 {
		offset = 0
	}
	keyword = strings.TrimSpace(keyword)
	var supplierArg any
	if supplierID != "" {
		supplierArg = supplierID
	}
	where := ` WHERE ($1='' OR r.dedicated_ip ILIKE '%'||$1||'%' OR r.assigned_ips ILIKE '%'||$1||'%' OR r.configuration ILIKE '%'||$1||'%' OR r.notes ILIKE '%'||$1||'%')
  AND ($2::text IS NULL OR sp.public_id::text=$2)
  AND ($3='' OR r.status=$3)`
	rows, err := s.DB.Query(ctx, manualResourceCols+where+` ORDER BY r.id DESC LIMIT $4 OFFSET $5`, keyword, supplierArg, status, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []ManualResource{}
	for rows.Next() {
		v, _, err := scanManualResource(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	var total int64
	if err := s.DB.QueryRow(ctx, `SELECT count(*) FROM manual_resources r LEFT JOIN manual_suppliers sp ON sp.id=r.supplier_id`+where, keyword, supplierArg, status).Scan(&total); err != nil {
		return nil, 0, err
	}
	return out, total, nil
}

// GetManualResource 资源详情。
func (s *Store) GetManualResource(ctx context.Context, publicID string) (ManualResource, error) {
	v, _, err := scanManualResource(s.DB.QueryRow(ctx, manualResourceCols+` WHERE r.public_id=$1`, publicID))
	if errors.Is(err, pgx.ErrNoRows) {
		return ManualResource{}, ErrNotFound
	}
	return v, err
}

// CreateManualResource 新增资源（默认空闲）。
func (s *Store) CreateManualResource(ctx context.Context, in ManualResourceInput) (ManualResource, error) {
	if strings.TrimSpace(in.DedicatedIP) == "" {
		return ManualResource{}, ErrInvalidState
	}
	if in.ControlMode != "ipmi" && in.ControlMode != "client" {
		return ManualResource{}, ErrInvalidState
	}
	if in.IpmiPort <= 0 {
		in.IpmiPort = 623
	}
	var supplierID *int64
	if in.SupplierID != "" {
		id, err := wanyunPublicToID(ctx, s.DB, "manual_suppliers", in.SupplierID)
		if err != nil {
			return ManualResource{}, err
		}
		supplierID = &id
	}
	var publicID string
	err := s.DB.QueryRow(ctx, `INSERT INTO manual_resources(dedicated_ip,assigned_ips,notes,configuration,cost_cents,username,password,
control_mode,ipmi_ip,ipmi_port,ipmi_version,dcim_client_url,dcim_client_id,control_username,control_password,due_time,supplier_id)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17) RETURNING public_id::text`,
		strings.TrimSpace(in.DedicatedIP), in.AssignedIPs, in.Notes, in.Configuration, in.CostCents, in.Username, in.Password,
		in.ControlMode, strings.TrimSpace(in.IpmiIP), in.IpmiPort, in.IpmiVersion, in.DcimClientURL, in.DcimClientID,
		in.ControlUsername, in.ControlPassword, in.DueTime, supplierID).Scan(&publicID)
	if err != nil {
		return ManualResource{}, err
	}
	return s.GetManualResource(ctx, publicID)
}

// UpdateManualResource 修改资源基础信息（分配关系走 Assign / Idle）。
func (s *Store) UpdateManualResource(ctx context.Context, publicID string, in ManualResourceInput) (ManualResource, error) {
	if in.ControlMode != "ipmi" && in.ControlMode != "client" {
		return ManualResource{}, ErrInvalidState
	}
	if in.IpmiPort <= 0 {
		in.IpmiPort = 623
	}
	var supplierID *int64
	if in.SupplierID != "" {
		id, err := wanyunPublicToID(ctx, s.DB, "manual_suppliers", in.SupplierID)
		if err != nil {
			return ManualResource{}, err
		}
		supplierID = &id
	}
	tag, err := s.DB.Exec(ctx, `UPDATE manual_resources SET dedicated_ip=$2,assigned_ips=$3,notes=$4,configuration=$5,cost_cents=$6,
username=$7,password=$8,control_mode=$9,ipmi_ip=$10,ipmi_port=$11,ipmi_version=$12,dcim_client_url=$13,dcim_client_id=$14,
control_username=$15,control_password=$16,due_time=$17,supplier_id=$18,updated_at=now() WHERE public_id=$1`,
		publicID, strings.TrimSpace(in.DedicatedIP), in.AssignedIPs, in.Notes, in.Configuration, in.CostCents, in.Username, in.Password,
		in.ControlMode, strings.TrimSpace(in.IpmiIP), in.IpmiPort, in.IpmiVersion, in.DcimClientURL, in.DcimClientID,
		in.ControlUsername, in.ControlPassword, in.DueTime, supplierID)
	if err != nil {
		return ManualResource{}, err
	}
	if tag.RowsAffected() == 0 {
		return ManualResource{}, ErrNotFound
	}
	return s.GetManualResource(ctx, publicID)
}

// DeleteManualResource 删除资源。
func (s *Store) DeleteManualResource(ctx context.Context, publicID string) error {
	tag, err := s.DB.Exec(ctx, `DELETE FROM manual_resources WHERE public_id=$1`, publicID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// AssignManualResource 分配资源到某用户名下的产品（状态置 assigned）。
// 客户由服务行带出（服务必须属于某个用户且未终止）。
func (s *Store) AssignManualResource(ctx context.Context, publicID, servicePublicID string, dueTime *time.Time) (ManualResource, error) {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return ManualResource{}, err
	}
	defer tx.Rollback(ctx)
	var id int64
	err = tx.QueryRow(ctx, `SELECT id FROM manual_resources WHERE public_id=$1 FOR UPDATE`, publicID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return ManualResource{}, ErrNotFound
	}
	if err != nil {
		return ManualResource{}, err
	}
	var userID, serviceID int64
	err = tx.QueryRow(ctx, `SELECT id,user_id FROM services WHERE public_id=$1 AND status NOT IN ('terminated','failed')`, servicePublicID).Scan(&serviceID, &userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ManualResource{}, ErrInvalidState
	}
	if err != nil {
		return ManualResource{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE manual_resources SET user_id=$2,service_id=$3,status='assigned',due_time=coalesce($4,due_time),updated_at=now() WHERE id=$1`,
		id, userID, serviceID, dueTime); err != nil {
		return ManualResource{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return ManualResource{}, err
	}
	return s.GetManualResource(ctx, publicID)
}

// IdleManualResource 释放资源（解除与客户 / 产品的关联，状态置 idle）。
func (s *Store) IdleManualResource(ctx context.Context, publicID string) (ManualResource, error) {
	tag, err := s.DB.Exec(ctx, `UPDATE manual_resources SET user_id=NULL,service_id=NULL,status='idle',updated_at=now() WHERE public_id=$1`, publicID)
	if err != nil {
		return ManualResource{}, err
	}
	if tag.RowsAffected() == 0 {
		return ManualResource{}, ErrNotFound
	}
	return s.GetManualResource(ctx, publicID)
}

// ManualPower 执行电源操作并落最近一次电源状态。
// mode=ipmi：经 internal/ipmi 对 BMC 下发；mode=client：明确不支持。
func (s *Store) ManualPower(ctx context.Context, publicID, action string) (ManualResource, ipmi.ChassisStatus, error) {
	r, err := s.GetManualResource(ctx, publicID)
	if err != nil {
		return r, ipmi.ChassisStatus{}, err
	}
	if r.ControlMode != "ipmi" || r.IpmiIP == "" {
		return r, ipmi.ChassisStatus{}, ErrManualUnsupported
	}
	client := &ipmi.Client{
		Addr:     net.JoinHostPort(strings.TrimSpace(r.IpmiIP), strconv.Itoa(r.IpmiPort)),
		Username: r.ControlUsername,
		Password: r.ControlPassword,
		Timeout:  5 * time.Second,
	}
	var power ipmi.ChassisStatus
	powerErr := error(nil)
	switch action {
	case "status":
		power, powerErr = client.Status(ctx)
	case "on":
		powerErr = client.PowerOn(ctx)
		if powerErr == nil {
			power, powerErr = client.Status(ctx)
		}
	case "off":
		powerErr = client.PowerOff(ctx)
		if powerErr == nil {
			power, powerErr = client.Status(ctx)
		}
	case "reboot":
		if powerErr = client.PowerCycle(ctx); powerErr == nil {
			power, powerErr = client.Status(ctx)
		}
	default:
		return r, power, ErrInvalidState
	}
	state := "error"
	if powerErr == nil {
		state = "off"
		if power.PowerOn {
			state = "on"
		}
	}
	if _, err := s.DB.Exec(ctx, `UPDATE manual_resources SET power_status=$2,updated_at=now() WHERE public_id=$1`, publicID, state); err != nil {
		return r, power, err
	}
	out, err := s.GetManualResource(ctx, publicID)
	if powerErr != nil {
		return out, power, powerErr
	}
	return out, power, nil
}
