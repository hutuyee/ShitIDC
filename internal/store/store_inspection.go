package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// 异常巡查记录（对齐魔方 CBAP 插件 abnormal_inspection_records）。
//
// 记录关联的用户与产品（服务）、异常时 IP、异常事项 / 处理措施 / 处理时间，
// 以及若干张异常截图。列表与详情的信息大部分来自关联表（用户资料、服务、
// 订单），这里按 join 实时读出，避免像插件那样冗余落库导致资料过期。
// 契约中 pay_time 取订单支付时间（orders.paid_at，未支付为 null）。

// InspectionImage 是一张异常截图的存储引用。
type InspectionImage struct {
	Stored string `json:"stored"`
	Name   string `json:"name"`
}

// AbnormalInspectionRecord 是一条异常巡查记录（列表与详情共用）。
type AbnormalInspectionRecord struct {
	PublicID    string            `json:"id"`
	ClientID    string            `json:"client_id"`
	Username    string            `json:"username"`
	Company     string            `json:"company"`
	Email       string            `json:"email"`
	Phone       string            `json:"phone"`
	PhoneCode   string            `json:"phone_code"`
	HostID      string            `json:"host_id"`
	ProductName string            `json:"product_name"`
	HostName    string            `json:"host_name"`
	OrderID     string            `json:"order_id"`
	PayTime     *time.Time        `json:"pay_time"`
	IP          string            `json:"ip"`
	Matter      string            `json:"matter"`
	Measure     string            `json:"measure"`
	ProcessTime time.Time         `json:"process_time"`
	Images      []InspectionImage `json:"img"`
	AdminName   string            `json:"admin_name"`
	CreatedAt   time.Time         `json:"created_at"`
	UpdatedAt   time.Time         `json:"updated_at"`
}

// AbnormalInspectionFilter 是异常巡查记录的筛选条件。
type AbnormalInspectionFilter struct {
	Keywords  string
	StartTime *time.Time
	EndTime   *time.Time
	Page      int
	Limit     int
}

// AbnormalInspectionInput 是新增/修改记录的表单。
type AbnormalInspectionInput struct {
	ClientPublicID  string
	ServicePublicID string
	IP              string
	Matter          string
	Measure         string
	ProcessTime     time.Time
	Images          []InspectionImage
}

const inspectionSelect = `SELECT r.public_id::text,u.public_id::text,coalesce(u.nickname,''),coalesce(up.company,''),u.email,coalesce(u.phone,''),
s.public_id::text,p.name,o.public_id::text,o.paid_at,r.ip,r.matter,r.measure,r.process_time,r.images,coalesce(au.nickname,''),r.created_at,r.updated_at
FROM abnormal_inspection_records r
JOIN users u ON u.id=r.user_id
LEFT JOIN user_profiles up ON up.user_id=u.id
JOIN services s ON s.id=r.service_id
JOIN products p ON p.id=s.product_id
JOIN orders o ON o.id=s.order_id
LEFT JOIN users au ON au.id=r.admin_id`

func scanInspection(row pgx.Row) (AbnormalInspectionRecord, error) {
	var v AbnormalInspectionRecord
	var raw []byte
	err := row.Scan(&v.PublicID, &v.ClientID, &v.Username, &v.Company, &v.Email, &v.Phone,
		&v.HostID, &v.ProductName, &v.OrderID, &v.PayTime, &v.IP, &v.Matter, &v.Measure,
		&v.ProcessTime, &raw, &v.AdminName, &v.CreatedAt, &v.UpdatedAt)
	if err != nil {
		return v, err
	}
	v.Images = []InspectionImage{}
	if len(raw) > 0 {
		if uerr := json.Unmarshal(raw, &v.Images); uerr != nil {
			return v, uerr
		}
	}
	return v, nil
}

// inspectionWhere 组装筛选条件（关键词匹配用户 / 公司 / 联系方式 / IP）。
func inspectionWhere(f AbnormalInspectionFilter) (string, []any) {
	args := []any{}
	where := []string{}
	if f.Keywords != "" {
		args = append(args, "%"+f.Keywords+"%")
		n := fmt.Sprintf("$%d", len(args))
		where = append(where, "(u.nickname ILIKE "+n+" OR u.email ILIKE "+n+" OR u.phone ILIKE "+n+" OR coalesce(up.company,'') ILIKE "+n+" OR r.ip ILIKE "+n+")")
	}
	if f.StartTime != nil {
		args = append(args, *f.StartTime)
		where = append(where, fmt.Sprintf("r.process_time>=$%d", len(args)))
	}
	if f.EndTime != nil {
		args = append(args, *f.EndTime)
		where = append(where, fmt.Sprintf("r.process_time<=$%d", len(args)))
	}
	if len(where) == 0 {
		return "", args
	}
	return " WHERE " + strings.Join(where, " AND "), args
}

// ListAbnormalInspectionRecords 分页列出异常巡查记录（处理时间倒序）。
func (s *Store) ListAbnormalInspectionRecords(ctx context.Context, f AbnormalInspectionFilter) ([]AbnormalInspectionRecord, int64, error) {
	if f.Page < 1 {
		f.Page = 1
	}
	if f.Limit < 1 || f.Limit > 200 {
		f.Limit = 20
	}
	cond, args := inspectionWhere(f)
	var count int64
	if err := s.DB.QueryRow(ctx, `SELECT count(*) FROM abnormal_inspection_records r
JOIN users u ON u.id=r.user_id LEFT JOIN user_profiles up ON up.user_id=u.id`+cond, args...).Scan(&count); err != nil {
		return nil, 0, err
	}
	pageArgs := append(append([]any{}, args...), f.Limit, (f.Page-1)*f.Limit)
	q := inspectionSelect + cond + fmt.Sprintf(" ORDER BY r.process_time DESC,r.id DESC LIMIT $%d OFFSET $%d", len(args)+1, len(args)+2)
	rows, err := s.DB.Query(ctx, q, pageArgs...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []AbnormalInspectionRecord{}
	for rows.Next() {
		v, err := scanInspection(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, v)
	}
	return out, count, rows.Err()
}

// ListAbnormalInspectionRecordsForExport 按筛选条件取全量（导出用）。
func (s *Store) ListAbnormalInspectionRecordsForExport(ctx context.Context, f AbnormalInspectionFilter) ([]AbnormalInspectionRecord, error) {
	cond, args := inspectionWhere(f)
	rows, err := s.DB.Query(ctx, inspectionSelect+cond+` ORDER BY r.process_time DESC,r.id DESC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AbnormalInspectionRecord{}
	for rows.Next() {
		v, err := scanInspection(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// GetAbnormalInspectionRecord 读取一条记录（含截图引用）。
func (s *Store) GetAbnormalInspectionRecord(ctx context.Context, publicID string) (AbnormalInspectionRecord, error) {
	v, err := scanInspection(s.DB.QueryRow(ctx, inspectionSelect+` WHERE r.public_id=$1`, publicID))
	if errors.Is(err, pgx.ErrNoRows) {
		return AbnormalInspectionRecord{}, ErrNotFound
	}
	if err != nil {
		return AbnormalInspectionRecord{}, err
	}
	return v, nil
}

// GetAbnormalInspectionImage 取一条记录第 index 张截图的存储引用（按列表顺序）。
func (s *Store) GetAbnormalInspectionImage(ctx context.Context, publicID string, index int) (InspectionImage, error) {
	rec, err := s.GetAbnormalInspectionRecord(ctx, publicID)
	if err != nil {
		return InspectionImage{}, err
	}
	if index < 0 || index >= len(rec.Images) {
		return InspectionImage{}, ErrNotFound
	}
	return rec.Images[index], nil
}

// validInspectionStored 校验截图存储引用：本地文件名或对象存储 key，防目录穿越。
func validInspectionStored(stored string) bool {
	key := stored
	if strings.HasPrefix(key, "oss:") {
		key = strings.TrimPrefix(key, "oss:")
		if !strings.HasPrefix(key, "uploads/inspection/") {
			return false
		}
		key = strings.TrimPrefix(key, "uploads/inspection/")
	}
	if key == "" || len(key) > 128 || strings.Contains(key, "..") {
		return false
	}
	for _, r := range key {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '_' || r == '-') {
			return false
		}
	}
	return true
}

// resolveInspectionRefs 校验并整理录入的截图列表（最多 9 张）。
func resolveInspectionRefs(in []InspectionImage) ([]InspectionImage, error) {
	if len(in) > 9 {
		return nil, errors.New("异常截图最多 9 张")
	}
	out := []InspectionImage{}
	for _, img := range in {
		stored := strings.TrimSpace(img.Stored)
		if !validInspectionStored(stored) {
			return nil, errors.New("异常截图引用不合法")
		}
		name := strings.TrimSpace(img.Name)
		if len(name) > 128 {
			name = name[:128]
		}
		if name == "" {
			name = "截图"
		}
		out = append(out, InspectionImage{Stored: stored, Name: name})
	}
	return out, nil
}

// CreateAbnormalInspectionRecord 新增一条异常巡查记录。
func (s *Store) CreateAbnormalInspectionRecord(ctx context.Context, adminID int64, in AbnormalInspectionInput) (AbnormalInspectionRecord, error) {
	userID, serviceID, err := s.resolveInspectionTargets(ctx, in)
	if err != nil {
		return AbnormalInspectionRecord{}, err
	}
	images, err := resolveInspectionRefs(in.Images)
	if err != nil {
		return AbnormalInspectionRecord{}, err
	}
	raw, err := json.Marshal(images)
	if err != nil {
		return AbnormalInspectionRecord{}, err
	}
	var publicID string
	err = s.DB.QueryRow(ctx, `INSERT INTO abnormal_inspection_records(user_id,service_id,ip,matter,measure,process_time,images,admin_id)
VALUES($1,$2,$3,$4,$5,$6,$7,nullif($8,0)::bigint) RETURNING public_id::text`,
		userID, serviceID, in.IP, in.Matter, in.Measure, in.ProcessTime, raw, adminID).Scan(&publicID)
	if err != nil {
		return AbnormalInspectionRecord{}, err
	}
	return s.GetAbnormalInspectionRecord(ctx, publicID)
}

// UpdateAbnormalInspectionRecord 修改一条记录。
func (s *Store) UpdateAbnormalInspectionRecord(ctx context.Context, publicID string, adminID int64, in AbnormalInspectionInput) error {
	userID, serviceID, err := s.resolveInspectionTargets(ctx, in)
	if err != nil {
		return err
	}
	images, err := resolveInspectionRefs(in.Images)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(images)
	if err != nil {
		return err
	}
	tag, err := s.DB.Exec(ctx, `UPDATE abnormal_inspection_records
SET user_id=$2,service_id=$3,ip=$4,matter=$5,measure=$6,process_time=$7,images=$8,admin_id=nullif($9,0)::bigint,updated_at=now()
WHERE public_id=$1`, publicID, userID, serviceID, in.IP, in.Matter, in.Measure, in.ProcessTime, raw, adminID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteAbnormalInspectionRecord 删除一条记录（截图文件保留，便于回溯）。
func (s *Store) DeleteAbnormalInspectionRecord(ctx context.Context, publicID string) error {
	tag, err := s.DB.Exec(ctx, `DELETE FROM abnormal_inspection_records WHERE public_id=$1`, publicID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// resolveInspectionTargets 解析用户与服务 public id，并校验服务属于该用户。
func (s *Store) resolveInspectionTargets(ctx context.Context, in AbnormalInspectionInput) (int64, int64, error) {
	var userID int64
	if err := s.DB.QueryRow(ctx, `SELECT id FROM users WHERE public_id=$1 AND deleted_at IS NULL`, in.ClientPublicID).Scan(&userID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, 0, errors.New("用户不存在")
		}
		return 0, 0, err
	}
	var serviceID int64
	if err := s.DB.QueryRow(ctx, `SELECT id FROM services WHERE public_id=$1 AND user_id=$2`, in.ServicePublicID, userID).Scan(&serviceID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, 0, errors.New("产品不存在或不属于该用户")
		}
		return 0, 0, err
	}
	return userID, serviceID, nil
}

// ValidInspectionStoredFileName 判断存储引用是否是合法的本地截图文件名
// （非 oss: 前缀、无路径分隔、无 ..），供读取接口做目录穿越防护。
func ValidInspectionStoredFileName(stored string) bool {
	return !strings.HasPrefix(stored, "oss:") && validInspectionStored(stored)
}
