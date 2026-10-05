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

// 内部工单（对齐魔方 CBAP TicketInternalPremium 插件）。
//
// 内部工单在管理端流转：管理员发起并指定部门与工单类型，部门人员可接单、回复、
// 转单、处理完成与关闭；工单按部门类型配置的处理时限（小时）计算「已超时 /
// 即将超时」（剩余不足 15% 时限），支持内部备注、预设回复、操作日志、评分
// （发起人评分 / 主管评分）与统计排名；定时工单按周期自动创建内部工单。

const ticketInternalBaseSelect = `
SELECT t.id, t.title, t.department_id, d.name, t.type_id, ty.name, ty.processing_limit,
t.status_id, st.name, st.color, st.finished, t.priority,
t.creator_id, coalesce(nullif(cp.nickname,''), cu.email),
coalesce(t.acceptor_id,0), coalesce(nullif(ap.nickname,''), au.email),
coalesce(t.assignee_id,0), coalesce(nullif(sp.nickname,''), su.email),
coalesce(t.last_reply_admin_id,0), coalesce(nullif(rp.nickname,''), ru.email),
t.last_reply_at, t.created_at, t.finish_at,
coalesce(t.client_id,0), coalesce(nullif(clp.nickname,''), clu.email),
t.satisfaction, t.attitude, t.processing_score,
t.director_satisfaction, t.director_attitude, t.director_processing_score,
coalesce(d.director_admin_id,0)
FROM ticket_internal_tickets t
JOIN ticket_internal_departments d ON d.id=t.department_id
JOIN ticket_internal_types ty ON ty.id=t.type_id
JOIN ticket_internal_statuses st ON st.id=t.status_id
JOIN users cu ON cu.id=t.creator_id
LEFT JOIN user_profiles cp ON cp.user_id=t.creator_id
LEFT JOIN users au ON au.id=t.acceptor_id
LEFT JOIN user_profiles ap ON ap.user_id=t.acceptor_id
LEFT JOIN users su ON su.id=t.assignee_id
LEFT JOIN user_profiles sp ON sp.user_id=t.assignee_id
LEFT JOIN users ru ON ru.id=t.last_reply_admin_id
LEFT JOIN user_profiles rp ON rp.user_id=t.last_reply_admin_id
LEFT JOIN users clu ON clu.id=t.client_id
LEFT JOIN user_profiles clp ON clp.user_id=t.client_id`

// TicketInternalStaff 是可被指派的管理人员（拥有内部工单权限的用户）。
type TicketInternalStaff struct {
	ID    int64  `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email"`
}

// TicketInternalType 是部门下的工单类型（带处理时限，单位小时）。
type TicketInternalType struct {
	ID              int64  `json:"id"`
	DepartmentID    int64  `json:"department_id"`
	Name            string `json:"name"`
	ProcessingLimit int    `json:"processing_limit"`
}

// TicketInternalDepartment 是工单部门（管理人员 / 主管 / 类型）。
type TicketInternalDepartment struct {
	ID              int64                 `json:"id"`
	Name            string                `json:"name"`
	AdminIDs        []int64               `json:"admin_ids"`
	Admin           []TicketInternalStaff `json:"admin"`
	DirectorAdminID int64                 `json:"director_admin_id"`
	DirectorName    string                `json:"director_admin_name"`
	Types           []TicketInternalType  `json:"type"`
}

// TicketInternalStatus 是工单状态（默认状态不可编辑 / 删除）。
type TicketInternalStatus struct {
	ID       int64  `json:"id"`
	Key      string `json:"key,omitempty"`
	Name     string `json:"name"`
	Color    string `json:"color"`
	Finished bool   `json:"status"`
	System   bool   `json:"noEdit"`
	Sort     int    `json:"sort"`
	Count    int64  `json:"count"`
}

// TicketInternalPrereply 是预设回复。
type TicketInternalPrereply struct {
	ID        int64     `json:"id"`
	Content   string    `json:"content"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// TicketInternalConfig 是「其他设置」。
type TicketInternalConfig struct {
	OrderButton       string `json:"order_button"`
	FollowLimit       string `json:"follow_limit"`
	WillTimeoutNotice string `json:"will_timeout_notice"`
	RefreshTime       string `json:"refresh_time"`
}

// TicketInternalHost 是内部工单关联的产品。
type TicketInternalHost struct {
	ID          string `json:"id"`
	ServiceID   string `json:"service_id"`
	ProductName string `json:"product_name"`
	Status      string `json:"status"`
}

// ListTicketInternalStaff 返回拥有内部工单权限的管理人员。
func (s *Store) ListTicketInternalStaff(ctx context.Context) ([]TicketInternalStaff, error) {
	rows, err := s.DB.Query(ctx, `SELECT u.id, coalesce(nullif(p.nickname,''),u.email), u.email
FROM users u
LEFT JOIN user_profiles p ON p.user_id=u.id
WHERE u.deleted_at IS NULL AND u.status='active' AND EXISTS (
  SELECT 1 FROM user_roles ur
  JOIN role_permissions rp ON rp.role_id=ur.role_id
  JOIN permissions pe ON pe.id=rp.permission_id
  WHERE ur.user_id=u.id AND pe.name='ticket_internal.manage')
ORDER BY u.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TicketInternalStaff{}
	for rows.Next() {
		var v TicketInternalStaff
		if err := rows.Scan(&v.ID, &v.Name, &v.Email); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Store) ticketInternalStaffNames(ctx context.Context, ids []int64) (map[int64]TicketInternalStaff, error) {
	out := map[int64]TicketInternalStaff{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := s.DB.Query(ctx, `SELECT u.id, coalesce(nullif(p.nickname,''),u.email), u.email
FROM users u LEFT JOIN user_profiles p ON p.user_id=u.id
WHERE u.id = ANY($1::bigint[])`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var v TicketInternalStaff
		if err := rows.Scan(&v.ID, &v.Name, &v.Email); err != nil {
			return nil, err
		}
		out[v.ID] = v
	}
	return out, rows.Err()
}

// ListTicketInternalDepartments 返回部门及其类型（供列表 / 选择器使用）。
func (s *Store) ListTicketInternalDepartments(ctx context.Context) ([]TicketInternalDepartment, error) {
	rows, err := s.DB.Query(ctx, `SELECT id, name, coalesce(admin_ids,'{}'), coalesce(director_admin_id,0)
FROM ticket_internal_departments ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TicketInternalDepartment{}
	ids := []int64{}
	for rows.Next() {
		var v TicketInternalDepartment
		if err := rows.Scan(&v.ID, &v.Name, &v.AdminIDs, &v.DirectorAdminID); err != nil {
			return nil, err
		}
		if v.DirectorAdminID > 0 {
			ids = append(ids, v.DirectorAdminID)
		}
		ids = append(ids, v.AdminIDs...)
		v.Admin = []TicketInternalStaff{}
		v.Types = []TicketInternalType{}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	names, err := s.ticketInternalStaffNames(ctx, ids)
	if err != nil {
		return nil, err
	}
	types, err := s.listTicketInternalTypes(ctx)
	if err != nil {
		return nil, err
	}
	for i := range out {
		for _, id := range out[i].AdminIDs {
			if st, ok := names[id]; ok {
				out[i].Admin = append(out[i].Admin, st)
			}
		}
		if st, ok := names[out[i].DirectorAdminID]; ok {
			out[i].DirectorName = st.Name
		}
		for _, ty := range types {
			if ty.DepartmentID == out[i].ID {
				out[i].Types = append(out[i].Types, ty)
			}
		}
	}
	return out, nil
}

func (s *Store) listTicketInternalTypes(ctx context.Context) ([]TicketInternalType, error) {
	rows, err := s.DB.Query(ctx, `SELECT id, department_id, name, processing_limit
FROM ticket_internal_types ORDER BY department_id, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TicketInternalType{}
	for rows.Next() {
		var v TicketInternalType
		if err := rows.Scan(&v.ID, &v.DepartmentID, &v.Name, &v.ProcessingLimit); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// TicketInternalDepartmentInput 是部门新增 / 编辑入参。
type TicketInternalDepartmentInput struct {
	Name            string
	AdminIDs        []int64
	DirectorAdminID int64
	Types           []TicketInternalTypeInput
}

// TicketInternalTypeInput 是部门下的类型行（name + 处理时限小时）。
type TicketInternalTypeInput struct {
	ID              int64
	Name            string
	ProcessingLimit int
}

// ServicesForUser 返回用户名下产品（内部工单关联产品选择器用）。
func (s *Store) ServicesForUser(ctx context.Context, userID int64) ([]TicketInternalHost, error) {
	rows, err := s.DB.Query(ctx, `SELECT s.id, s.public_id::text, p.name, s.status
FROM services s JOIN products p ON p.id=s.product_id
WHERE s.user_id=$1 ORDER BY s.created_at DESC LIMIT 500`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TicketInternalHost{}
	for rows.Next() {
		var v TicketInternalHost
		var id int64
		if err := rows.Scan(&id, &v.ServiceID, &v.ProductName, &v.Status); err != nil {
			return nil, err
		}
		v.ID = fmt.Sprintf("s-%d", id)
		out = append(out, v)
	}
	return out, rows.Err()
}

// ResolveUserByPublicID 在关联用户时把公开 ID / 数字 UID / 邮箱统一解析为内部 ID。
func (s *Store) ResolveUserByPublicID(ctx context.Context, ref string) (int64, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return 0, ErrNotFound
	}
	var id int64
	err := s.DB.QueryRow(ctx, `SELECT id FROM users
WHERE deleted_at IS NULL AND (public_id::text=$1 OR id::text=$1 OR email ILIKE $1)`, ref).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrNotFound
	}
	return id, err
}

// TicketInternalFilter 是内部工单列表筛选条件。
type TicketInternalFilter struct {
	Keywords         string
	TypeIDs          []int64
	StatusIDs        []int64
	CreatorID        int64
	LastReplyAdminID int64
	AcceptorID       int64
	Page             int
	Limit            int
	ViewerID         int64
}

// TicketInternalListRow 是内部工单列表行。
type TicketInternalListRow struct {
	ID             int64      `json:"id"`
	TicketNum      string     `json:"ticket_num"`
	Title          string     `json:"title"`
	DepartmentID   int64      `json:"department_id"`
	DepartmentName string     `json:"department_name"`
	TypeID         int64      `json:"type_id"`
	TypeName       string     `json:"type_name"`
	Priority       string     `json:"priority"`
	StatusID       int64      `json:"status_id"`
	StatusName     string     `json:"status"`
	Color          string     `json:"color"`
	Finished       bool       `json:"finished"`
	Timeout        int        `json:"timeout"`
	CreatorID      int64      `json:"post_admin_id"`
	CreatorName    string     `json:"post_admin_name"`
	AcceptorID     int64      `json:"order_admin_id"`
	AcceptorName   string     `json:"order_admin_name"`
	AssigneeID     int64      `json:"assignee_id"`
	AssigneeName   string     `json:"assignee_name"`
	LastReplyID    int64      `json:"last_reply_admin_id"`
	LastReplyName  string     `json:"last_reply_admin_name"`
	LastReplyAt    *time.Time `json:"last_reply_time"`
	ClientID       int64      `json:"client_id"`
	ClientName     string     `json:"client_name"`
	ShowScore      bool       `json:"show_score"`
	ScoreRole      string     `json:"score_role"`
	CreatedAt      time.Time  `json:"created_at"`
	FinishAt       *time.Time `json:"finish_at"`

	Satisfaction            *float64 `json:"satisfaction"`
	Attitude                *float64 `json:"attitude"`
	ProcessingScore         *float64 `json:"processing_score"`
	DirectorSatisfaction    *float64 `json:"director_satisfaction"`
	DirectorAttitude        *float64 `json:"director_attitude"`
	DirectorProcessingScore *float64 `json:"director_processing_score"`
}

func ticketInternalNum(id int64, createdAt time.Time) string {
	return fmt.Sprintf("TI%s%05d", createdAt.Format("20060102"), id)
}

func (s *Store) scanTicketInternalRow(row pgx.Row) (TicketInternalListRow, int, bool, int64, error) {
	var v TicketInternalListRow
	var processingLimit int
	var systemFinished bool
	var directorID int64
	err := row.Scan(&v.ID, &v.Title, &v.DepartmentID, &v.DepartmentName, &v.TypeID, &v.TypeName, &processingLimit,
		&v.StatusID, &v.StatusName, &v.Color, &systemFinished, &v.Priority,
		&v.CreatorID, &v.CreatorName, &v.AcceptorID, &v.AcceptorName,
		&v.AssigneeID, &v.AssigneeName, &v.LastReplyID, &v.LastReplyName,
		&v.LastReplyAt, &v.CreatedAt, &v.FinishAt,
		&v.ClientID, &v.ClientName, &v.Satisfaction, &v.Attitude, &v.ProcessingScore,
		&v.DirectorSatisfaction, &v.DirectorAttitude, &v.DirectorProcessingScore, &directorID)
	if err != nil {
		return v, 0, false, 0, err
	}
	v.TicketNum = ticketInternalNum(v.ID, v.CreatedAt)
	v.Finished = systemFinished || v.FinishAt != nil
	return v, processingLimit, systemFinished, directorID, nil
}

func ticketInternalDecorate(v *TicketInternalListRow, processingLimit int, viewerID, directorID int64) {
	now := time.Now()
	deadline := v.CreatedAt.Add(time.Duration(processingLimit) * time.Hour)
	switch {
	case v.Finished:
		if v.FinishAt != nil && v.FinishAt.After(deadline) {
			v.Timeout = 1
		}
	case processingLimit > 0 && now.After(deadline):
		v.Timeout = 1
	case processingLimit > 0 && now.After(v.CreatedAt.Add(time.Duration(float64(processingLimit)*0.85*float64(time.Hour)))):
		v.Timeout = 2
	}
	if !v.Finished {
		return
	}
	if viewerID > 0 && viewerID == v.CreatorID && v.Satisfaction == nil {
		v.ShowScore = true
		v.ScoreRole = "creator"
		return
	}
	if viewerID > 0 && directorID > 0 && viewerID == directorID && v.DirectorSatisfaction == nil {
		v.ShowScore = true
		v.ScoreRole = "director"
	}
}

// ListTicketInternalTickets 返回内部工单列表（含总数）。
func (s *Store) ListTicketInternalTickets(ctx context.Context, f TicketInternalFilter) ([]TicketInternalListRow, int64, error) {
	where := []string{"1=1"}
	args := []any{}
	add := func(v any) string {
		args = append(args, v)
		return fmt.Sprintf("$%d", len(args))
	}
	if kw := strings.TrimSpace(f.Keywords); kw != "" {
		p := add("%" + kw + "%")
		where = append(where, "(t.title ILIKE "+p+" OR t.content ILIKE "+p+" OR ('TI'||to_char(t.created_at,'YYYYMMDD')||lpad(t.id::text,5,'0')) ILIKE "+p+")")
	}
	if len(f.TypeIDs) > 0 {
		where = append(where, "t.type_id = ANY("+add(f.TypeIDs)+"::bigint[])")
	}
	if len(f.StatusIDs) > 0 {
		where = append(where, "t.status_id = ANY("+add(f.StatusIDs)+"::bigint[])")
	}
	if f.CreatorID > 0 {
		where = append(where, "t.creator_id = "+add(f.CreatorID))
	}
	if f.LastReplyAdminID > 0 {
		where = append(where, "t.last_reply_admin_id = "+add(f.LastReplyAdminID))
	}
	if f.AcceptorID > 0 {
		where = append(where, "t.acceptor_id = "+add(f.AcceptorID))
	}
	clause := strings.Join(where, " AND ")
	var total int64
	if err := s.DB.QueryRow(ctx, "SELECT count(*) FROM ticket_internal_tickets t WHERE "+clause, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	limit := f.Limit
	if limit <= 0 || limit > 200 {
		limit = 20
	}
	page := f.Page
	if page < 1 {
		page = 1
	}
	q := ticketInternalBaseSelect + "\nWHERE " + clause + `
ORDER BY (st.finished OR t.finish_at IS NOT NULL) ASC, COALESCE(t.last_reply_at,t.created_at) DESC, t.id DESC
LIMIT ` + add(limit) + ` OFFSET ` + add((page-1)*limit)
	rows, err := s.DB.Query(ctx, q, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []TicketInternalListRow{}
	for rows.Next() {
		v, pl, _, directorID, err := s.scanTicketInternalRow(rows)
		if err != nil {
			return nil, 0, err
		}
		ticketInternalDecorate(&v, pl, f.ViewerID, directorID)
		out = append(out, v)
	}
	return out, total, rows.Err()
}

// TicketInternalCreateInput 是新建内部工单入参。
type TicketInternalCreateInput struct {
	Title        string
	DepartmentID int64
	TypeID       int64
	ClientID     int64
	HostIDs      []int64
	SourceTicket string
	Priority     string
	Content      string
	Notes        string
	Attachments  json.RawMessage
	CreatorID    int64
	AssigneeID   int64
	SourceCronID int64
}

// CreateTicketInternal 新建内部工单并写日志，返回工单 ID 与编号。
func (s *Store) CreateTicketInternal(ctx context.Context, in TicketInternalCreateInput) (int64, string, error) {
	in.Title = strings.TrimSpace(in.Title)
	if in.Title == "" {
		return 0, "", errors.New("工单标题不能为空")
	}
	if len([]rune(in.Title)) > 150 {
		return 0, "", errors.New("工单标题不能超过 150 字")
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return 0, "", err
	}
	defer tx.Rollback(ctx)
	err = tx.QueryRow(ctx, `SELECT 1 FROM ticket_internal_types ty
JOIN ticket_internal_departments d ON d.id=ty.department_id
WHERE ty.id=$1 AND ty.department_id=$2`, in.TypeID, in.DepartmentID).Scan(new(int))
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, "", errors.New("工单部门或类型不存在")
	}
	if err != nil {
		return 0, "", err
	}
	var statusID int64
	if err := tx.QueryRow(ctx, `SELECT id FROM ticket_internal_statuses WHERE key='wait'`).Scan(&statusID); err != nil {
		return 0, "", err
	}
	var sourceID *int64
	if strings.TrimSpace(in.SourceTicket) != "" {
		var id int64
		if err := tx.QueryRow(ctx, `SELECT id FROM tickets WHERE public_id::text=$1`, strings.TrimSpace(in.SourceTicket)).Scan(&id); err == nil {
			sourceID = &id
		}
	}
	hosts := in.HostIDs
	if hosts == nil {
		hosts = []int64{}
	}
	if len(in.Attachments) == 0 {
		in.Attachments = json.RawMessage("[]")
	}
	var clientID, assignee, cronID *int64
	if in.ClientID > 0 {
		clientID = &in.ClientID
	}
	if in.AssigneeID > 0 {
		assignee = &in.AssigneeID
	}
	if in.SourceCronID > 0 {
		cronID = &in.SourceCronID
	}
	priority := "normal"
	if in.Priority == "urgent" {
		priority = "urgent"
	}
	var id int64
	var createdAt time.Time
	err = tx.QueryRow(ctx, `INSERT INTO ticket_internal_tickets
(title,department_id,type_id,status_id,creator_id,client_id,host_ids,source_ticket_id,priority,content,notes,attachments,assignee_id,cron_id)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14) RETURNING id,created_at`,
		in.Title, in.DepartmentID, in.TypeID, statusID, in.CreatorID, clientID, hosts, sourceID, priority, in.Content, in.Notes, in.Attachments, assignee, cronID).Scan(&id, &createdAt)
	if err != nil {
		return 0, "", err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO ticket_internal_logs(ticket_id,admin_id,description) VALUES($1,$2,$3)`, id, in.CreatorID, "创建了内部工单"); err != nil {
		return 0, "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, "", err
	}
	return id, ticketInternalNum(id, createdAt), nil
}

// TicketInternalReply 是内部工单的回复。
type TicketInternalReply struct {
	ID          int64           `json:"id"`
	AdminID     int64           `json:"admin_id"`
	AdminName   string          `json:"admin_name"`
	Content     string          `json:"content"`
	Attachments json.RawMessage `json:"attachment"`
	CreatedAt   time.Time       `json:"created_at"`
	UpdatedAt   time.Time       `json:"updated_at"`
	Type        string          `json:"type"`
}

// TicketInternalNote 是内部备注。
type TicketInternalNote struct {
	ID        int64     `json:"id"`
	AdminID   int64     `json:"admin_id"`
	AdminName string    `json:"admin_name"`
	Content   string    `json:"content"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	Type      string    `json:"type"`
}

// TicketInternalLog 是工单操作日志。
type TicketInternalLog struct {
	ID          int64     `json:"id"`
	AdminID     int64     `json:"admin_id"`
	AdminName   string    `json:"admin_name"`
	Description string    `json:"description"`
	CreatedAt   time.Time `json:"create_time"`
}

// TicketInternalDetail 是内部工单详情。
type TicketInternalDetail struct {
	TicketInternalListRow
	Content            string                `json:"content"`
	Notes              string                `json:"notes"`
	Attachments        json.RawMessage       `json:"attachment"`
	ClientPublicID     string                `json:"client_public_id"`
	ClientEmail        string                `json:"client_email"`
	SourceTicketID     int64                 `json:"source_ticket_id"`
	SourceTicketPublic string                `json:"source_ticket_public_id"`
	SourceTicketTitle  string                `json:"source_ticket_title"`
	Hosts              []TicketInternalHost  `json:"hosts"`
	Replies            []TicketInternalReply `json:"replies"`
	NotesList          []TicketInternalNote  `json:"notes_list"`
	Logs               []TicketInternalLog   `json:"logs"`
}

// GetTicketInternalTicket 读取内部工单详情（含回复 / 备注 / 日志）。
func (s *Store) GetTicketInternalTicket(ctx context.Context, id, viewerID int64) (TicketInternalDetail, error) {
	row := s.DB.QueryRow(ctx, ticketInternalBaseSelect+"\nWHERE t.id=$1", id)
	v, pl, _, directorID, err := s.scanTicketInternalRow(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return TicketInternalDetail{}, ErrNotFound
	}
	if err != nil {
		return TicketInternalDetail{}, err
	}
	ticketInternalDecorate(&v, pl, viewerID, directorID)
	d := TicketInternalDetail{TicketInternalListRow: v}
	var attachments []byte
	var hostIDs []int64
	err = s.DB.QueryRow(ctx, `SELECT t.content, t.notes, t.attachments, coalesce(t.host_ids,'{}'),
coalesce(cu.public_id::text,''), coalesce(cu.email,''),
coalesce(t.source_ticket_id,0), coalesce(st.public_id::text,''), coalesce(st.subject,'')
FROM ticket_internal_tickets t
LEFT JOIN users cu ON cu.id=t.client_id
LEFT JOIN tickets st ON st.id=t.source_ticket_id
WHERE t.id=$1`, id).Scan(&d.Content, &d.Notes, &attachments, &hostIDs, &d.ClientPublicID, &d.ClientEmail, &d.SourceTicketID, &d.SourceTicketPublic, &d.SourceTicketTitle)
	if err != nil {
		return TicketInternalDetail{}, err
	}
	d.Attachments = json.RawMessage(attachments)
	d.Hosts = []TicketInternalHost{}
	if len(hostIDs) > 0 {
		rows, err := s.DB.Query(ctx, `SELECT s.id, s.public_id::text, p.name, s.status
FROM services s JOIN products p ON p.id=s.product_id WHERE s.id = ANY($1::bigint[])`, hostIDs)
		if err != nil {
			return TicketInternalDetail{}, err
		}
		hostMap := map[int64]TicketInternalHost{}
		for rows.Next() {
			var svcID int64
			var h TicketInternalHost
			if err := rows.Scan(&svcID, &h.ServiceID, &h.ProductName, &h.Status); err != nil {
				rows.Close()
				return TicketInternalDetail{}, err
			}
			h.ID = fmt.Sprintf("s-%d", svcID)
			hostMap[svcID] = h
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return TicketInternalDetail{}, err
		}
		for _, svcID := range hostIDs {
			if h, ok := hostMap[svcID]; ok {
				d.Hosts = append(d.Hosts, h)
			}
		}
	}
	replies, err := s.listTicketInternalReplies(ctx, id)
	if err != nil {
		return TicketInternalDetail{}, err
	}
	d.Replies = replies
	notes, err := s.listTicketInternalNotes(ctx, id)
	if err != nil {
		return TicketInternalDetail{}, err
	}
	d.NotesList = notes
	logs, err := s.ListTicketInternalLogs(ctx, id)
	if err != nil {
		return TicketInternalDetail{}, err
	}
	d.Logs = logs
	return d, nil
}

func (s *Store) listTicketInternalReplies(ctx context.Context, ticketID int64) ([]TicketInternalReply, error) {
	rows, err := s.DB.Query(ctx, `SELECT r.id, coalesce(r.admin_id,0), coalesce(nullif(p.nickname,''),u.email,''),
r.content, r.attachments, r.created_at, r.updated_at
FROM ticket_internal_replies r
LEFT JOIN users u ON u.id=r.admin_id
LEFT JOIN user_profiles p ON p.user_id=r.admin_id
WHERE r.ticket_id=$1 ORDER BY r.created_at, r.id`, ticketID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TicketInternalReply{}
	for rows.Next() {
		var v TicketInternalReply
		var raw []byte
		if err := rows.Scan(&v.ID, &v.AdminID, &v.AdminName, &v.Content, &raw, &v.CreatedAt, &v.UpdatedAt); err != nil {
			return nil, err
		}
		v.Attachments = json.RawMessage(raw)
		v.Type = "Admin"
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Store) listTicketInternalNotes(ctx context.Context, ticketID int64) ([]TicketInternalNote, error) {
	rows, err := s.DB.Query(ctx, `SELECT n.id, coalesce(n.admin_id,0), coalesce(nullif(p.nickname,''),u.email,''),
n.content, n.created_at, n.updated_at
FROM ticket_internal_notes n
LEFT JOIN users u ON u.id=n.admin_id
LEFT JOIN user_profiles p ON p.user_id=n.admin_id
WHERE n.ticket_id=$1 ORDER BY n.created_at, n.id`, ticketID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TicketInternalNote{}
	for rows.Next() {
		var v TicketInternalNote
		if err := rows.Scan(&v.ID, &v.AdminID, &v.AdminName, &v.Content, &v.CreatedAt, &v.UpdatedAt); err != nil {
			return nil, err
		}
		v.Type = "notes"
		out = append(out, v)
	}
	return out, rows.Err()
}

// ListTicketInternalLogs 返回工单日志（倒序）。
func (s *Store) ListTicketInternalLogs(ctx context.Context, ticketID int64) ([]TicketInternalLog, error) {
	rows, err := s.DB.Query(ctx, `SELECT l.id, coalesce(l.admin_id,0), coalesce(nullif(p.nickname,''),u.email,''),
l.description, l.created_at
FROM ticket_internal_logs l
LEFT JOIN users u ON u.id=l.admin_id
LEFT JOIN user_profiles p ON p.user_id=l.admin_id
WHERE l.ticket_id=$1 ORDER BY l.id DESC LIMIT 500`, ticketID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TicketInternalLog{}
	for rows.Next() {
		var v TicketInternalLog
		if err := rows.Scan(&v.ID, &v.AdminID, &v.AdminName, &v.Description, &v.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func ticketInternalConfigValue(ctx context.Context, tx pgx.Tx, key string) string {
	var v string
	if err := tx.QueryRow(ctx, `SELECT v FROM ticket_internal_config WHERE k=$1`, key).Scan(&v); err != nil {
		return ""
	}
	return v
}

// ReplyTicketInternal 追加一条内部工单回复。
func (s *Store) ReplyTicketInternal(ctx context.Context, ticketID, adminID int64, content string, attachments json.RawMessage) (TicketInternalReply, error) {
	content = strings.TrimSpace(content)
	if content == "" {
		return TicketInternalReply{}, errors.New("回复内容不能为空")
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return TicketInternalReply{}, err
	}
	defer tx.Rollback(ctx)
	var finished bool
	var acceptorID, assigneeID int64
	err = tx.QueryRow(ctx, `SELECT (st.finished OR t.finish_at IS NOT NULL), coalesce(t.acceptor_id,0), coalesce(t.assignee_id,0)
FROM ticket_internal_tickets t JOIN ticket_internal_statuses st ON st.id=t.status_id
WHERE t.id=$1 FOR UPDATE OF t`, ticketID).Scan(&finished, &acceptorID, &assigneeID)
	if errors.Is(err, pgx.ErrNoRows) {
		return TicketInternalReply{}, ErrNotFound
	}
	if err != nil {
		return TicketInternalReply{}, err
	}
	if finished {
		return TicketInternalReply{}, errors.New("工单已完结，回复前请先变更状态")
	}
	if ticketInternalConfigValue(ctx, tx, "order_button") == "1" && acceptorID == 0 {
		return TicketInternalReply{}, errors.New("请先接单后再回复")
	}
	if ticketInternalConfigValue(ctx, tx, "follow_limit") == "1" && assigneeID > 0 && assigneeID != adminID {
		return TicketInternalReply{}, errors.New("仅跟进人可以回复，请先转单")
	}
	if len(attachments) == 0 {
		attachments = json.RawMessage("[]")
	}
	var v TicketInternalReply
	err = tx.QueryRow(ctx, `INSERT INTO ticket_internal_replies(ticket_id,admin_id,content,attachments)
VALUES($1,$2,$3,$4) RETURNING id,created_at,updated_at`, ticketID, adminID, content, attachments).
		Scan(&v.ID, &v.CreatedAt, &v.UpdatedAt)
	if err != nil {
		return TicketInternalReply{}, err
	}
	v.AdminID = adminID
	v.Content = content
	v.Attachments = attachments
	v.Type = "Admin"
	if _, err := tx.Exec(ctx, `UPDATE ticket_internal_tickets SET last_reply_at=now(), last_reply_admin_id=$2,
assignee_id=COALESCE(assignee_id,$2), status_id=(SELECT id FROM ticket_internal_statuses WHERE key='replied'), updated_at=now()
WHERE id=$1`, ticketID, adminID); err != nil {
		return TicketInternalReply{}, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO ticket_internal_logs(ticket_id,admin_id,description) VALUES($1,$2,$3)`, ticketID, adminID, "回复了工单"); err != nil {
		return TicketInternalReply{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return TicketInternalReply{}, err
	}
	return v, nil
}

// UpdateTicketInternalReply 编辑回复内容。
func (s *Store) UpdateTicketInternalReply(ctx context.Context, replyID, adminID int64, content string) error {
	content = strings.TrimSpace(content)
	if content == "" {
		return errors.New("回复内容不能为空")
	}
	tag, err := s.DB.Exec(ctx, `UPDATE ticket_internal_replies SET content=$2, updated_at=now()
WHERE id=$1`, replyID, content)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteTicketInternalReply 删除回复。
func (s *Store) DeleteTicketInternalReply(ctx context.Context, replyID int64) error {
	tag, err := s.DB.Exec(ctx, `DELETE FROM ticket_internal_replies WHERE id=$1`, replyID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// AddTicketInternalNote 添加内部备注。
func (s *Store) AddTicketInternalNote(ctx context.Context, ticketID, adminID int64, content string) (TicketInternalNote, error) {
	content = strings.TrimSpace(content)
	if content == "" {
		return TicketInternalNote{}, errors.New("备注内容不能为空")
	}
	var v TicketInternalNote
	err := s.DB.QueryRow(ctx, `INSERT INTO ticket_internal_notes(ticket_id,admin_id,content)
VALUES($1,$2,$3) RETURNING id,created_at,updated_at`, ticketID, adminID, content).
		Scan(&v.ID, &v.CreatedAt, &v.UpdatedAt)
	if err != nil {
		return TicketInternalNote{}, err
	}
	v.AdminID = adminID
	v.Content = content
	v.Type = "notes"
	_, _ = s.DB.Exec(ctx, `INSERT INTO ticket_internal_logs(ticket_id,admin_id,description) VALUES($1,$2,$3)`, ticketID, adminID, "添加了备注")
	return v, nil
}

// UpdateTicketInternalNote 编辑内部备注。
func (s *Store) UpdateTicketInternalNote(ctx context.Context, noteID, adminID int64, content string) error {
	content = strings.TrimSpace(content)
	if content == "" {
		return errors.New("备注内容不能为空")
	}
	tag, err := s.DB.Exec(ctx, `UPDATE ticket_internal_notes SET content=$2, updated_at=now() WHERE id=$1`, noteID, content)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteTicketInternalNote 删除内部备注。
func (s *Store) DeleteTicketInternalNote(ctx context.Context, noteID int64) error {
	tag, err := s.DB.Exec(ctx, `DELETE FROM ticket_internal_notes WHERE id=$1`, noteID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// SaveTicketInternalDepartment 新增 / 编辑工单部门（含类型与处理时限）。
func (s *Store) SaveTicketInternalDepartment(ctx context.Context, id int64, in TicketInternalDepartmentInput) (int64, error) {
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" {
		return 0, errors.New("部门名称不能为空")
	}
	if len(in.AdminIDs) == 0 {
		return 0, errors.New("请选择部门管理人员")
	}
	if in.DirectorAdminID == 0 {
		return 0, errors.New("请选择部门主管")
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	var deptID int64
	if id > 0 {
		tag, err := tx.Exec(ctx, `UPDATE ticket_internal_departments SET name=$2, admin_ids=$3, director_admin_id=$4, updated_at=now() WHERE id=$1`,
			id, in.Name, in.AdminIDs, in.DirectorAdminID)
		if err != nil {
			return 0, err
		}
		if tag.RowsAffected() == 0 {
			return 0, ErrNotFound
		}
		deptID = id
		if _, err := tx.Exec(ctx, `DELETE FROM ticket_internal_types WHERE department_id=$1`, id); err != nil {
			return 0, err
		}
	} else {
		if err := tx.QueryRow(ctx, `INSERT INTO ticket_internal_departments(name,admin_ids,director_admin_id)
VALUES($1,$2,$3) RETURNING id`, in.Name, in.AdminIDs, in.DirectorAdminID).Scan(&deptID); err != nil {
			return 0, err
		}
	}
	for _, ty := range in.Types {
		name := strings.TrimSpace(ty.Name)
		if name == "" {
			continue
		}
		limit := ty.ProcessingLimit
		if limit <= 0 {
			limit = 24
		}
		if _, err := tx.Exec(ctx, `INSERT INTO ticket_internal_types(department_id,name,processing_limit) VALUES($1,$2,$3)`,
			deptID, name, limit); err != nil {
			return 0, err
		}
	}
	var n int64
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM ticket_internal_types WHERE department_id=$1`, deptID).Scan(&n); err != nil {
		return 0, err
	}
	if n == 0 {
		return 0, errors.New("请至少配置一个工单类型")
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return deptID, nil
}

// DeleteTicketInternalDepartment 删除部门（有工单或定时工单引用时拒绝）。
func (s *Store) DeleteTicketInternalDepartment(ctx context.Context, id int64) error {
	var n int64
	if err := s.DB.QueryRow(ctx, `SELECT
(SELECT count(*) FROM ticket_internal_tickets WHERE department_id=$1) +
(SELECT count(*) FROM ticket_internal_cron_jobs WHERE department_id=$1)`, id).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return errors.New("该部门已被内部工单或定时工单使用，无法删除")
	}
	tag, err := s.DB.Exec(ctx, `DELETE FROM ticket_internal_departments WHERE id=$1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ListTicketInternalStatuses 返回工单状态（含使用数量）。
func (s *Store) ListTicketInternalStatuses(ctx context.Context) ([]TicketInternalStatus, error) {
	rows, err := s.DB.Query(ctx, `SELECT st.id, coalesce(st.key,''), st.name, st.color, st.finished, st.system, st.sort,
(SELECT count(*) FROM ticket_internal_tickets t WHERE t.status_id=st.id)
FROM ticket_internal_statuses st ORDER BY st.sort, st.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TicketInternalStatus{}
	for rows.Next() {
		var v TicketInternalStatus
		if err := rows.Scan(&v.ID, &v.Key, &v.Name, &v.Color, &v.Finished, &v.System, &v.Sort, &v.Count); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// CreateTicketInternalStatus 新增自定义状态。
func (s *Store) CreateTicketInternalStatus(ctx context.Context, name, color string, finished bool) (int64, error) {
	name = strings.TrimSpace(name)
	color = strings.TrimSpace(color)
	if name == "" || color == "" {
		return 0, errors.New("状态名称与颜色为必填")
	}
	var id int64
	err := s.DB.QueryRow(ctx, `INSERT INTO ticket_internal_statuses(name,color,finished,system,sort)
VALUES($1,$2,$3,FALSE,(SELECT coalesce(max(sort),0)+1 FROM ticket_internal_statuses)) RETURNING id`,
		name, color, finished).Scan(&id)
	return id, err
}

// UpdateTicketInternalStatus 编辑状态（默认状态不可修改）。
func (s *Store) UpdateTicketInternalStatus(ctx context.Context, id int64, name, color string, finished bool) error {
	name = strings.TrimSpace(name)
	color = strings.TrimSpace(color)
	if name == "" || color == "" {
		return errors.New("状态名称与颜色为必填")
	}
	var system bool
	if err := s.DB.QueryRow(ctx, `SELECT system FROM ticket_internal_statuses WHERE id=$1`, id).Scan(&system); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if system {
		return errors.New("默认状态不可修改")
	}
	_, err := s.DB.Exec(ctx, `UPDATE ticket_internal_statuses SET name=$2,color=$3,finished=$4 WHERE id=$1`,
		id, name, color, finished)
	return err
}

// DeleteTicketInternalStatus 删除自定义状态。
func (s *Store) DeleteTicketInternalStatus(ctx context.Context, id int64) error {
	var system bool
	if err := s.DB.QueryRow(ctx, `SELECT system FROM ticket_internal_statuses WHERE id=$1`, id).Scan(&system); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if system {
		return errors.New("默认状态不可删除")
	}
	var used int64
	if err := s.DB.QueryRow(ctx, `SELECT count(*) FROM ticket_internal_tickets WHERE status_id=$1`, id).Scan(&used); err != nil {
		return err
	}
	if used > 0 {
		return errors.New("该状态已被内部工单使用，无法删除")
	}
	_, err := s.DB.Exec(ctx, `DELETE FROM ticket_internal_statuses WHERE id=$1`, id)
	return err
}

// ListTicketInternalPrereplies 返回预设回复。
func (s *Store) ListTicketInternalPrereplies(ctx context.Context) ([]TicketInternalPrereply, error) {
	rows, err := s.DB.Query(ctx, `SELECT id, content, created_at, updated_at FROM ticket_internal_prereplies ORDER BY id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TicketInternalPrereply{}
	for rows.Next() {
		var v TicketInternalPrereply
		if err := rows.Scan(&v.ID, &v.Content, &v.CreatedAt, &v.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// CreateTicketInternalPrereply 新增预设回复。
func (s *Store) CreateTicketInternalPrereply(ctx context.Context, content string) (int64, error) {
	content = strings.TrimSpace(content)
	if content == "" {
		return 0, errors.New("回复内容不能为空")
	}
	var id int64
	err := s.DB.QueryRow(ctx, `INSERT INTO ticket_internal_prereplies(content) VALUES($1) RETURNING id`, content).Scan(&id)
	return id, err
}

// UpdateTicketInternalPrereply 编辑预设回复。
func (s *Store) UpdateTicketInternalPrereply(ctx context.Context, id int64, content string) error {
	content = strings.TrimSpace(content)
	if content == "" {
		return errors.New("回复内容不能为空")
	}
	tag, err := s.DB.Exec(ctx, `UPDATE ticket_internal_prereplies SET content=$2, updated_at=now() WHERE id=$1`, id, content)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteTicketInternalPrereply 删除预设回复。
func (s *Store) DeleteTicketInternalPrereply(ctx context.Context, id int64) error {
	tag, err := s.DB.Exec(ctx, `DELETE FROM ticket_internal_prereplies WHERE id=$1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// GetTicketInternalConfig 读取「其他设置」。
func (s *Store) GetTicketInternalConfig(ctx context.Context) (TicketInternalConfig, error) {
	cfg := TicketInternalConfig{OrderButton: "0", FollowLimit: "0", WillTimeoutNotice: "0", RefreshTime: "180"}
	rows, err := s.DB.Query(ctx, `SELECT k,v FROM ticket_internal_config`)
	if err != nil {
		return cfg, err
	}
	defer rows.Close()
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return cfg, err
		}
		switch k {
		case "order_button":
			cfg.OrderButton = v
		case "follow_limit":
			cfg.FollowLimit = v
		case "will_timeout_notice":
			cfg.WillTimeoutNotice = v
		case "refresh_time":
			cfg.RefreshTime = v
		}
	}
	return cfg, rows.Err()
}

// SaveTicketInternalConfig 保存「其他设置」。
func (s *Store) SaveTicketInternalConfig(ctx context.Context, cfg TicketInternalConfig) error {
	items := map[string]string{
		"order_button":        cfg.OrderButton,
		"follow_limit":        cfg.FollowLimit,
		"will_timeout_notice": cfg.WillTimeoutNotice,
		"refresh_time":        cfg.RefreshTime,
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	for k, v := range items {
		if _, err := tx.Exec(ctx, `INSERT INTO ticket_internal_config(k,v) VALUES($1,$2)
ON CONFLICT (k) DO UPDATE SET v=excluded.v`, k, strings.TrimSpace(v)); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// AcceptTicketInternal 接单（领取）；待接单状态自动进入待回复。
func (s *Store) AcceptTicketInternal(ctx context.Context, ticketID, adminID int64) error {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var statusID int64
	var statusKey string
	err = tx.QueryRow(ctx, `SELECT t.status_id, st.key FROM ticket_internal_tickets t
JOIN ticket_internal_statuses st ON st.id=t.status_id WHERE t.id=$1 FOR UPDATE OF t`, ticketID).Scan(&statusID, &statusKey)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if statusKey == "wait" {
		if err := tx.QueryRow(ctx, `SELECT id FROM ticket_internal_statuses WHERE key='waiting'`).Scan(&statusID); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE ticket_internal_tickets SET acceptor_id=$2,
assignee_id=COALESCE(assignee_id,$2), status_id=$3, updated_at=now() WHERE id=$1`, ticketID, adminID, statusID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO ticket_internal_logs(ticket_id,admin_id,description) VALUES($1,$2,$3)`,
		ticketID, adminID, "接单"); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) ticketInternalIsStaff(ctx context.Context, tx pgx.Tx, userID int64) (bool, error) {
	var ok bool
	err := tx.QueryRow(ctx, `SELECT EXISTS (
  SELECT 1 FROM user_roles ur
  JOIN role_permissions rp ON rp.role_id=ur.role_id
  JOIN permissions pe ON pe.id=rp.permission_id
  WHERE ur.user_id=$1 AND pe.name='ticket_internal.manage')`, userID).Scan(&ok)
	return ok, err
}

// ForwardTicketInternal 转单：换部门 / 类型 / 处理人员，并留下转交备注。
func (s *Store) ForwardTicketInternal(ctx context.Context, ticketID, adminID, departmentID, typeID, assigneeID int64, reason string) error {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var deptName, typeName string
	err = tx.QueryRow(ctx, `SELECT d.name, ty.name FROM ticket_internal_types ty
JOIN ticket_internal_departments d ON d.id=ty.department_id
WHERE ty.id=$1 AND ty.department_id=$2`, typeID, departmentID).Scan(&deptName, &typeName)
	if errors.Is(err, pgx.ErrNoRows) {
		return errors.New("工单部门或类型不存在")
	}
	if err != nil {
		return err
	}
	var target *int64
	assigneeName := ""
	if assigneeID > 0 {
		ok, err := s.ticketInternalIsStaff(ctx, tx, assigneeID)
		if err != nil {
			return err
		}
		if !ok {
			return errors.New("指定的处理人员不存在或没有内部工单权限")
		}
		target = &assigneeID
		var name string
		if err := tx.QueryRow(ctx, `SELECT coalesce(nullif(p.nickname,''),u.email) FROM users u
LEFT JOIN user_profiles p ON p.user_id=u.id WHERE u.id=$1`, assigneeID).Scan(&name); err == nil {
			assigneeName = name
		}
	}
	tag, err := tx.Exec(ctx, `UPDATE ticket_internal_tickets SET department_id=$2, type_id=$3,
assignee_id=COALESCE($4,assignee_id), updated_at=now() WHERE id=$1`, ticketID, departmentID, typeID, target)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	desc := "转单到 " + deptName + " - " + typeName
	if assigneeName != "" {
		desc += "（" + assigneeName + "）"
	}
	if reason = strings.TrimSpace(reason); reason != "" {
		desc += "：" + reason
		if _, err := tx.Exec(ctx, `INSERT INTO ticket_internal_notes(ticket_id,admin_id,content) VALUES($1,$2,$3)`,
			ticketID, adminID, "转交备注："+reason); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO ticket_internal_logs(ticket_id,admin_id,description) VALUES($1,$2,$3)`,
		ticketID, adminID, desc); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// UpdateTicketInternal 保存详情页的修改（状态 / 部门类型 / 关联产品）。
func (s *Store) UpdateTicketInternal(ctx context.Context, ticketID, adminID, statusID, typeID int64, hostIDs []int64) error {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var parts []string
	args := []any{ticketID}
	add := func(expr string, v any) {
		args = append(args, v)
		parts = append(parts, fmt.Sprintf(expr, len(args)))
	}
	if statusID > 0 {
		var name string
		err := tx.QueryRow(ctx, `SELECT name FROM ticket_internal_statuses WHERE id=$1`, statusID).Scan(&name)
		if errors.Is(err, pgx.ErrNoRows) {
			return errors.New("工单状态不存在")
		}
		if err != nil {
			return err
		}
		add("status_id=$%d", statusID)
	}
	if typeID > 0 {
		var deptID int64
		err := tx.QueryRow(ctx, `SELECT department_id FROM ticket_internal_types WHERE id=$1`, typeID).Scan(&deptID)
		if errors.Is(err, pgx.ErrNoRows) {
			return errors.New("工单类型不存在")
		}
		if err != nil {
			return err
		}
		add("type_id=$%d", typeID)
		add("department_id=$%d", deptID)
	}
	if hostIDs != nil {
		add("host_ids=$%d", hostIDs)
	}
	if len(parts) == 0 {
		return nil
	}
	q := "UPDATE ticket_internal_tickets SET " + strings.Join(parts, ",") + ", updated_at=now() WHERE id=$1"
	tag, err := tx.Exec(ctx, q, args...)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	if _, err := tx.Exec(ctx, `INSERT INTO ticket_internal_logs(ticket_id,admin_id,description) VALUES($1,$2,$3)`,
		ticketID, adminID, "更新了工单"); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// CloseTicketInternal 关闭内部工单。
func (s *Store) CloseTicketInternal(ctx context.Context, ticketID, adminID int64) error {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `UPDATE ticket_internal_tickets SET
status_id=(SELECT id FROM ticket_internal_statuses WHERE key='closed'),
finish_at=COALESCE(finish_at,now()), updated_at=now() WHERE id=$1`, ticketID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	if _, err := tx.Exec(ctx, `INSERT INTO ticket_internal_logs(ticket_id,admin_id,description) VALUES($1,$2,$3)`,
		ticketID, adminID, "关闭了工单"); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// FinishTicketInternal 处理完成；close 为真时同时关闭工单。
func (s *Store) FinishTicketInternal(ctx context.Context, ticketID, adminID int64, close bool) error {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if close {
		_, err = tx.Exec(ctx, `UPDATE ticket_internal_tickets SET finish_at=now(),
status_id=(SELECT id FROM ticket_internal_statuses WHERE key='closed'), updated_at=now() WHERE id=$1`, ticketID)
	} else {
		_, err = tx.Exec(ctx, `UPDATE ticket_internal_tickets SET finish_at=now(), updated_at=now() WHERE id=$1`, ticketID)
	}
	if err != nil {
		return err
	}
	desc := "处理完成"
	if close {
		desc = "处理完成并关闭"
	}
	if _, err := tx.Exec(ctx, `INSERT INTO ticket_internal_logs(ticket_id,admin_id,description) VALUES($1,$2,$3)`,
		ticketID, adminID, desc); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ScoreTicketInternal 评分：发起人评分或部门主管评分（被评分主体为第一处理人）。
func (s *Store) ScoreTicketInternal(ctx context.Context, ticketID, adminID int64, satisfaction, attitude, processing float64) (string, error) {
	for _, v := range []float64{satisfaction, attitude, processing} {
		if v < 0.5 || v > 5 {
			return "", errors.New("评分最低为半星，最高 5 星")
		}
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)
	var creatorID int64
	var directorID int64
	var finished bool
	var creatorScored, directorScored bool
	err = tx.QueryRow(ctx, `SELECT t.creator_id, coalesce(d.director_admin_id,0),
(t.finish_at IS NOT NULL OR st.finished),
(t.satisfaction IS NOT NULL), (t.director_satisfaction IS NOT NULL)
FROM ticket_internal_tickets t
JOIN ticket_internal_departments d ON d.id=t.department_id
JOIN ticket_internal_statuses st ON st.id=t.status_id
WHERE t.id=$1 FOR UPDATE OF t`, ticketID).Scan(&creatorID, &directorID, &finished, &creatorScored, &directorScored)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	if !finished {
		return "", errors.New("工单完成后才能评分")
	}
	role := ""
	switch {
	case adminID == creatorID && !creatorScored:
		role = "creator"
	case adminID == directorID && directorID > 0 && !directorScored:
		role = "director"
	case adminID == creatorID:
		return "", errors.New("您已评分")
	case adminID == directorID:
		return "", errors.New("您已评分")
	default:
		return "", errors.New("仅发起人或部门主管可以评分")
	}
	if role == "creator" {
		_, err = tx.Exec(ctx, `UPDATE ticket_internal_tickets SET satisfaction=$2, attitude=$3, processing_score=$4, updated_at=now() WHERE id=$1`,
			ticketID, satisfaction, attitude, processing)
	} else {
		_, err = tx.Exec(ctx, `UPDATE ticket_internal_tickets SET director_satisfaction=$2, director_attitude=$3, director_processing_score=$4, updated_at=now() WHERE id=$1`,
			ticketID, satisfaction, attitude, processing)
	}
	if err != nil {
		return "", err
	}
	desc := "发起人评分"
	if role == "director" {
		desc = "主管评分"
	}
	if _, err := tx.Exec(ctx, `INSERT INTO ticket_internal_logs(ticket_id,admin_id,description) VALUES($1,$2,$3)`,
		ticketID, adminID, desc); err != nil {
		return "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return "", err
	}
	return role, nil
}

// TicketInternalStatsFilter 是工单统计筛选条件。
type TicketInternalStatsFilter struct {
	Type      string // all / department / admin
	ID        int64
	ScoreRole string // "" 全部 / 0 发起人评分 / 1 主管评分
	Start     *time.Time
	End       *time.Time
}

// TicketInternalStats 是工单统计聚合。
type TicketInternalStats struct {
	Total                 int64   `json:"total"`
	PendingTotal          int64   `json:"pending_total"`
	ProcessedTotal        int64   `json:"processed_total"`
	AverageProcessingTime float64 `json:"average_processing_time"`
	ScoreTotal            int64   `json:"score_total"`
	NotScoreTotal         int64   `json:"not_score_total"`
	ScoreRate             float64 `json:"score_rate"`
	OvertimeRatio         float64 `json:"overtime_ratio"`
	Satisfaction          float64 `json:"satisfaction"`
	Attitude              float64 `json:"attitude"`
	ProcessingTime        float64 `json:"processing_time"`
	AvgrageScore          float64 `json:"avgrage_score"`
}

func ticketInternalScoreCols(role, prefix string) (string, string, string) {
	switch role {
	case "0":
		return prefix + "satisfaction", prefix + "attitude", prefix + "processing_score"
	case "1":
		return prefix + "director_satisfaction", prefix + "director_attitude", prefix + "director_processing_score"
	default:
		return "coalesce(" + prefix + "satisfaction," + prefix + "director_satisfaction)",
			"coalesce(" + prefix + "attitude," + prefix + "director_attitude)",
			"coalesce(" + prefix + "processing_score," + prefix + "director_processing_score)"
	}
}

// TicketInternalStatistics 汇总工单量 / 处理时长 / 评分 / 超时占比。
func (s *Store) TicketInternalStatistics(ctx context.Context, f TicketInternalStatsFilter) (TicketInternalStats, error) {
	var st TicketInternalStats
	where := []string{"1=1"}
	args := []any{}
	add := func(v any) string {
		args = append(args, v)
		return fmt.Sprintf("$%d", len(args))
	}
	if f.Type == "department" && f.ID > 0 {
		where = append(where, "t.department_id="+add(f.ID))
	}
	if f.Type == "admin" && f.ID > 0 {
		p := add(f.ID)
		where = append(where, "(t.acceptor_id="+p+" OR t.assignee_id="+p+" OR t.last_reply_admin_id="+p+")")
	}
	if f.Start != nil {
		where = append(where, "t.created_at >= "+add(*f.Start))
	}
	if f.End != nil {
		where = append(where, "t.created_at <= "+add(*f.End))
	}
	satCol, attCol, psCol := ticketInternalScoreCols(f.ScoreRole, "t.")
	q := `SELECT t.created_at, t.finish_at, (st.finished OR t.finish_at IS NOT NULL) AS finished,
CASE WHEN t.finish_at IS NOT NULL THEN t.finish_at > t.created_at + (ty.processing_limit||' hours')::interval
     ELSE now() > t.created_at + (ty.processing_limit||' hours')::interval END AS overtime,
` + satCol + `, ` + attCol + `, ` + psCol + `
FROM ticket_internal_tickets t
JOIN ticket_internal_types ty ON ty.id=t.type_id
JOIN ticket_internal_statuses st ON st.id=t.status_id
WHERE ` + strings.Join(where, " AND ")
	rows, err := s.DB.Query(ctx, q, args...)
	if err != nil {
		return st, err
	}
	defer rows.Close()
	var sumProcessing float64
	var processingCount int64
	var scored int64
	var sumSat, sumAtt, sumPS float64
	var overtimeCount int64
	for rows.Next() {
		var createdAt time.Time
		var finishAt *time.Time
		var finished, overtime bool
		var sat, att, ps *float64
		if err := rows.Scan(&createdAt, &finishAt, &finished, &overtime, &sat, &att, &ps); err != nil {
			return st, err
		}
		st.Total++
		if overtime {
			overtimeCount++
		}
		if finished {
			st.ProcessedTotal++
			if finishAt != nil {
				sumProcessing += finishAt.Sub(createdAt).Seconds()
				processingCount++
			}
		} else {
			st.PendingTotal++
		}
		if sat != nil && att != nil && ps != nil {
			scored++
			sumSat += *sat
			sumAtt += *att
			sumPS += *ps
		}
	}
	if err := rows.Err(); err != nil {
		return st, err
	}
	st.ScoreTotal = scored
	if st.ProcessedTotal > scored {
		st.NotScoreTotal = st.ProcessedTotal - scored
	}
	if st.Total > 0 {
		st.OvertimeRatio = float64(overtimeCount) / float64(st.Total)
	}
	if st.ProcessedTotal > 0 {
		st.ScoreRate = float64(scored) / float64(st.ProcessedTotal)
	}
	if processingCount > 0 {
		st.AverageProcessingTime = sumProcessing / float64(processingCount)
	}
	if scored > 0 {
		st.Satisfaction = sumSat / float64(scored)
		st.Attitude = sumAtt / float64(scored)
		st.ProcessingTime = sumPS / float64(scored)
		st.AvgrageScore = (st.Satisfaction + st.Attitude + st.ProcessingTime) / 3
	}
	return st, nil
}

// TicketInternalRankRow 是排名行。
type TicketInternalRankRow struct {
	Name           string  `json:"name"`
	Score          float64 `json:"score"`
	Satisfaction   float64 `json:"satisfaction"`
	Attitude       float64 `json:"attitude"`
	ProcessingTime float64 `json:"processing_time"`
}

// TicketInternalScoreRank 平均分排名：按部门或按个人（被评分主体为第一处理人）。
func (s *Store) TicketInternalScoreRank(ctx context.Context, byDepartment bool, departmentID int64, scoreRole string, start, end *time.Time) ([]TicketInternalRankRow, error) {
	satCol, attCol, psCol := ticketInternalScoreCols(scoreRole, "t.")
	nameExpr := `coalesce(nullif(sp.nickname,''), su.email, '未指定')`
	join := `LEFT JOIN users su ON su.id=COALESCE(t.acceptor_id,t.assignee_id,t.last_reply_admin_id)
LEFT JOIN user_profiles sp ON sp.user_id=su.id`
	if byDepartment {
		nameExpr = "d.name"
		join = ""
	}
	where := []string{satCol + " IS NOT NULL"}
	args := []any{}
	add := func(v any) string {
		args = append(args, v)
		return fmt.Sprintf("$%d", len(args))
	}
	if departmentID > 0 {
		where = append(where, "t.department_id="+add(departmentID))
	}
	if start != nil {
		where = append(where, "t.created_at >= "+add(*start))
	}
	if end != nil {
		where = append(where, "t.created_at <= "+add(*end))
	}
	q := `SELECT x.name, avg(x.sat), avg(x.att), avg(x.ps) FROM (
SELECT ` + nameExpr + ` AS name, ` + satCol + ` AS sat, ` + attCol + ` AS att, ` + psCol + ` AS ps
FROM ticket_internal_tickets t
JOIN ticket_internal_departments d ON d.id=t.department_id
` + join + `
WHERE ` + strings.Join(where, " AND ") + `
) x GROUP BY x.name ORDER BY (avg(x.sat)+avg(x.att)+avg(x.ps))/3 DESC LIMIT 100`
	rows, err := s.DB.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TicketInternalRankRow{}
	for rows.Next() {
		var v TicketInternalRankRow
		if err := rows.Scan(&v.Name, &v.Satisfaction, &v.Attitude, &v.ProcessingTime); err != nil {
			return nil, err
		}
		v.Score = (v.Satisfaction + v.Attitude + v.ProcessingTime) / 3
		out = append(out, v)
	}
	return out, rows.Err()
}

// TicketInternalTimeRank 平均处理时长排名（秒）：按部门或按个人。
func (s *Store) TicketInternalTimeRank(ctx context.Context, byDepartment bool, departmentID int64, start, end *time.Time) ([]TicketInternalRankRow, error) {
	nameExpr := `coalesce(nullif(sp.nickname,''), su.email, '未指定')`
	join := `LEFT JOIN users su ON su.id=COALESCE(t.acceptor_id,t.assignee_id,t.last_reply_admin_id)
LEFT JOIN user_profiles sp ON sp.user_id=su.id`
	if byDepartment {
		nameExpr = "d.name"
		join = ""
	}
	where := []string{"t.finish_at IS NOT NULL"}
	args := []any{}
	add := func(v any) string {
		args = append(args, v)
		return fmt.Sprintf("$%d", len(args))
	}
	if departmentID > 0 {
		where = append(where, "t.department_id="+add(departmentID))
	}
	if start != nil {
		where = append(where, "t.created_at >= "+add(*start))
	}
	if end != nil {
		where = append(where, "t.created_at <= "+add(*end))
	}
	q := `SELECT x.name, avg(x.secs) FROM (
SELECT ` + nameExpr + ` AS name, EXTRACT(EPOCH FROM (t.finish_at - t.created_at)) AS secs
FROM ticket_internal_tickets t
JOIN ticket_internal_departments d ON d.id=t.department_id
` + join + `
WHERE ` + strings.Join(where, " AND ") + `
) x GROUP BY x.name ORDER BY avg(x.secs) ASC LIMIT 100`
	rows, err := s.DB.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TicketInternalRankRow{}
	for rows.Next() {
		var v TicketInternalRankRow
		if err := rows.Scan(&v.Name, &v.Score); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// TicketInternalCronJob 是定时工单。
type TicketInternalCronJob struct {
	ID             int64      `json:"id"`
	Title          string     `json:"title"`
	Content        string     `json:"content"`
	CyclePeriod    int        `json:"cycle_period"`
	Unit           string     `json:"unit"`
	StartAt        time.Time  `json:"start_time"`
	EndAt          *time.Time `json:"end_time"`
	TriggerTime    string     `json:"trigger_time"`
	DepartmentID   int64      `json:"department_id"`
	DepartmentName string     `json:"department_name"`
	TypeID         int64      `json:"type_id"`
	TypeName       string     `json:"type_name"`
	AdminID        int64      `json:"admin_id"`
	AdminName      string     `json:"admin_name"`
	CreatorID      int64      `json:"create_admin_id"`
	CreatorName    string     `json:"create_admin_name"`
	Status         int        `json:"status"`
	NextRunAt      *time.Time `json:"next_create_time"`
	LastRunAt      *time.Time `json:"last_run_at"`
	CreatedAt      time.Time  `json:"created_at"`
}

// TicketInternalCronInput 是定时工单新增 / 编辑入参。
type TicketInternalCronInput struct {
	Title        string
	Content      string
	CyclePeriod  int
	Unit         string
	StartAt      time.Time
	EndAt        *time.Time
	TriggerTime  string
	DepartmentID int64
	TypeID       int64
	AdminID      int64
	CreatorID    int64
	Status       int
}

const ticketInternalCronSelect = `
SELECT c.id, c.title, c.content, c.cycle_period, c.unit, c.start_at, c.end_at, c.trigger_time,
c.department_id, d.name, c.type_id, ty.name, coalesce(c.admin_id,0), coalesce(nullif(ap.nickname,''), au.email, ''),
c.creator_id, coalesce(nullif(cp.nickname,''), cu.email, ''), c.status, c.next_run_at, c.last_run_at, c.created_at
FROM ticket_internal_cron_jobs c
JOIN ticket_internal_departments d ON d.id=c.department_id
JOIN ticket_internal_types ty ON ty.id=c.type_id
LEFT JOIN users au ON au.id=c.admin_id
LEFT JOIN user_profiles ap ON ap.user_id=c.admin_id
JOIN users cu ON cu.id=c.creator_id
LEFT JOIN user_profiles cp ON cp.user_id=c.creator_id`

func scanTicketInternalCron(row pgx.Row) (TicketInternalCronJob, error) {
	var v TicketInternalCronJob
	err := row.Scan(&v.ID, &v.Title, &v.Content, &v.CyclePeriod, &v.Unit, &v.StartAt, &v.EndAt, &v.TriggerTime,
		&v.DepartmentID, &v.DepartmentName, &v.TypeID, &v.TypeName, &v.AdminID, &v.AdminName,
		&v.CreatorID, &v.CreatorName, &v.Status, &v.NextRunAt, &v.LastRunAt, &v.CreatedAt)
	return v, err
}

// ListTicketInternalCronJobs 分页返回定时工单。
func (s *Store) ListTicketInternalCronJobs(ctx context.Context, page, limit int) ([]TicketInternalCronJob, int64, error) {
	if limit <= 0 || limit > 200 {
		limit = 20
	}
	if page < 1 {
		page = 1
	}
	var total int64
	if err := s.DB.QueryRow(ctx, `SELECT count(*) FROM ticket_internal_cron_jobs`).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.DB.Query(ctx, ticketInternalCronSelect+` ORDER BY c.id DESC LIMIT $1 OFFSET $2`, limit, (page-1)*limit)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []TicketInternalCronJob{}
	for rows.Next() {
		v, err := scanTicketInternalCron(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, v)
	}
	return out, total, rows.Err()
}

// GetTicketInternalCronJob 读取单条定时工单。
func (s *Store) GetTicketInternalCronJob(ctx context.Context, id int64) (TicketInternalCronJob, error) {
	v, err := scanTicketInternalCron(s.DB.QueryRow(ctx, ticketInternalCronSelect+` WHERE c.id=$1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return TicketInternalCronJob{}, ErrNotFound
	}
	return v, err
}

func parseTicketInternalTrigger(trigger string) (int, int) {
	trigger = strings.TrimSpace(trigger)
	var hh, mm int
	if _, err := fmt.Sscanf(trigger, "%d:%d", &hh, &mm); err != nil {
		return 9, 0
	}
	if hh < 0 || hh > 23 {
		hh = 9
	}
	if mm < 0 || mm > 59 {
		mm = 0
	}
	return hh, mm
}

// ticketInternalNextRun 计算下一次触发时间；周期 <=0 表示一次性，超过结束时间返回 nil。
func ticketInternalNextRun(start time.Time, trigger string, period int, unit string, end *time.Time, from time.Time) *time.Time {
	hh, mm := parseTicketInternalTrigger(trigger)
	first := time.Date(start.Year(), start.Month(), start.Day(), hh, mm, 0, 0, start.Location())
	if period <= 0 {
		if !first.After(from) || (end != nil && first.After(*end)) {
			return nil
		}
		return &first
	}
	next := first
	for !next.After(from) {
		switch unit {
		case "month":
			next = next.AddDate(0, period, 0)
		case "year":
			next = next.AddDate(period, 0, 0)
		default:
			next = next.AddDate(0, 0, period)
		}
		if end != nil && next.After(*end) {
			return nil
		}
	}
	return &next
}

func (s *Store) ticketInternalCronValidate(ctx context.Context, in *TicketInternalCronInput) error {
	in.Title = strings.TrimSpace(in.Title)
	if in.Title == "" {
		return errors.New("工单标题不能为空")
	}
	switch in.Unit {
	case "day", "month", "year":
	default:
		return errors.New("周期类型只支持天 / 自然月 / 年")
	}
	if in.CyclePeriod < 0 {
		return errors.New("循环周期不能为负数")
	}
	if in.StartAt.IsZero() {
		return errors.New("请选择日期范围")
	}
	var ok bool
	if err := s.DB.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM ticket_internal_types WHERE id=$1 AND department_id=$2)`,
		in.TypeID, in.DepartmentID).Scan(&ok); err != nil {
		return err
	}
	if !ok {
		return errors.New("工单部门或类型不存在")
	}
	if in.AdminID > 0 {
		var staff bool
		if err := s.DB.QueryRow(ctx, `SELECT EXISTS (
  SELECT 1 FROM user_roles ur
  JOIN role_permissions rp ON rp.role_id=ur.role_id
  JOIN permissions pe ON pe.id=rp.permission_id
  WHERE ur.user_id=$1 AND pe.name='ticket_internal.manage')`, in.AdminID).Scan(&staff); err != nil {
			return err
		}
		if !staff {
			return errors.New("指定的处理人员不存在或没有内部工单权限")
		}
	}
	if in.TriggerTime = strings.TrimSpace(in.TriggerTime); in.TriggerTime == "" {
		in.TriggerTime = "09:00"
	}
	return nil
}

// CreateTicketInternalCronJob 新建定时工单。
func (s *Store) CreateTicketInternalCronJob(ctx context.Context, in TicketInternalCronInput) (int64, error) {
	if err := s.ticketInternalCronValidate(ctx, &in); err != nil {
		return 0, err
	}
	next := ticketInternalNextRun(in.StartAt, in.TriggerTime, in.CyclePeriod, in.Unit, in.EndAt, time.Now())
	status := in.Status
	if next == nil {
		status = 0
	}
	var id int64
	err := s.DB.QueryRow(ctx, `INSERT INTO ticket_internal_cron_jobs
(title,content,cycle_period,unit,start_at,end_at,trigger_time,department_id,type_id,admin_id,creator_id,status,next_run_at)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13) RETURNING id`,
		in.Title, in.Content, in.CyclePeriod, in.Unit, in.StartAt, in.EndAt, in.TriggerTime, in.DepartmentID, in.TypeID,
		nullInt64(in.AdminID), in.CreatorID, status, next).Scan(&id)
	return id, err
}

func nullInt64(v int64) any {
	if v <= 0 {
		return nil
	}
	return v
}

// UpdateTicketInternalCronJob 编辑定时工单。
func (s *Store) UpdateTicketInternalCronJob(ctx context.Context, id int64, in TicketInternalCronInput) error {
	if err := s.ticketInternalCronValidate(ctx, &in); err != nil {
		return err
	}
	next := ticketInternalNextRun(in.StartAt, in.TriggerTime, in.CyclePeriod, in.Unit, in.EndAt, time.Now())
	status := in.Status
	if next == nil {
		status = 0
	}
	tag, err := s.DB.Exec(ctx, `UPDATE ticket_internal_cron_jobs SET
title=$2, content=$3, cycle_period=$4, unit=$5, start_at=$6, end_at=$7, trigger_time=$8,
department_id=$9, type_id=$10, admin_id=$11, status=$12, next_run_at=$13, updated_at=now()
WHERE id=$1`,
		id, in.Title, in.Content, in.CyclePeriod, in.Unit, in.StartAt, in.EndAt, in.TriggerTime,
		in.DepartmentID, in.TypeID, nullInt64(in.AdminID), status, next)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// SetTicketInternalCronStatus 启停定时工单；重新开启时从当前时间起计算下次触发。
func (s *Store) SetTicketInternalCronStatus(ctx context.Context, id int64, status int) error {
	job, err := s.GetTicketInternalCronJob(ctx, id)
	if err != nil {
		return err
	}
	var next *time.Time
	if status == 1 {
		next = ticketInternalNextRun(job.StartAt, job.TriggerTime, job.CyclePeriod, job.Unit, job.EndAt, time.Now())
		if next == nil {
			status = 0
		}
	}
	_, err = s.DB.Exec(ctx, `UPDATE ticket_internal_cron_jobs SET status=$2, next_run_at=$3, updated_at=now() WHERE id=$1`, id, status, next)
	return err
}

// DeleteTicketInternalCronJob 删除定时工单。
func (s *Store) DeleteTicketInternalCronJob(ctx context.Context, id int64) error {
	tag, err := s.DB.Exec(ctx, `DELETE FROM ticket_internal_cron_jobs WHERE id=$1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// RunDueTicketInternalCronJobs 执行到期的定时工单并推进下次触发时间。
func (s *Store) RunDueTicketInternalCronJobs(ctx context.Context, limit int) (int, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	rows, err := s.DB.Query(ctx, `SELECT id,title,content,cycle_period,unit,start_at,end_at,trigger_time,
department_id,type_id,coalesce(admin_id,0),creator_id
FROM ticket_internal_cron_jobs WHERE status=1 AND next_run_at IS NOT NULL AND next_run_at<=now()
ORDER BY next_run_at LIMIT $1`, limit)
	if err != nil {
		return 0, err
	}
	type due struct {
		id               int64
		title, content   string
		period           int
		unit, trigger    string
		start            time.Time
		end              *time.Time
		department, typ  int64
		adminID, creator int64
	}
	items := []due{}
	for rows.Next() {
		var d due
		if err := rows.Scan(&d.id, &d.title, &d.content, &d.period, &d.unit, &d.start, &d.end, &d.trigger,
			&d.department, &d.typ, &d.adminID, &d.creator); err != nil {
			rows.Close()
			return 0, err
		}
		items = append(items, d)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	created := 0
	for _, d := range items {
		ticketID, _, err := s.CreateTicketInternal(ctx, TicketInternalCreateInput{
			Title:        d.title,
			DepartmentID: d.department,
			TypeID:       d.typ,
			Content:      d.content,
			CreatorID:    d.creator,
			AssigneeID:   d.adminID,
			SourceCronID: d.id,
		})
		if err != nil {
			return created, err
		}
		_, _ = s.DB.Exec(ctx, `INSERT INTO ticket_internal_logs(ticket_id,admin_id,description) VALUES($1,$2,$3)`,
			ticketID, d.creator, "定时工单自动创建")
		next := ticketInternalNextRun(d.start, d.trigger, d.period, d.unit, d.end, time.Now())
		status := 1
		if next == nil {
			status = 0
		}
		if _, err := s.DB.Exec(ctx, `UPDATE ticket_internal_cron_jobs SET next_run_at=$2, last_run_at=now(), status=$3, updated_at=now() WHERE id=$1`,
			d.id, next, status); err != nil {
			return created, err
		}
		created++
	}
	return created, nil
}

// RunTicketInternalTimeoutReminders 对剩余不足 15% 处理时限的工单给处理人发站内提醒。
func (s *Store) RunTicketInternalTimeoutReminders(ctx context.Context, limit int) (int, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	var enabled bool
	if err := s.DB.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM ticket_internal_config WHERE k='will_timeout_notice' AND v='1')`).Scan(&enabled); err != nil {
		return 0, err
	}
	if !enabled {
		return 0, nil
	}
	rows, err := s.DB.Query(ctx, `SELECT t.id, t.title, coalesce(t.client_id,0), coalesce(t.acceptor_id,0), coalesce(t.assignee_id,0)
FROM ticket_internal_tickets t
JOIN ticket_internal_types ty ON ty.id=t.type_id
JOIN ticket_internal_statuses st ON st.id=t.status_id
WHERE NOT (st.finished OR t.finish_at IS NOT NULL) AND t.reminded_at IS NULL
AND ty.processing_limit > 0
AND now() > t.created_at + ((ty.processing_limit * 0.85)||' hours')::interval
ORDER BY t.created_at LIMIT $1`, limit)
	if err != nil {
		return 0, err
	}
	type item struct {
		id, clientID, acceptorID, assigneeID int64
		title                                string
	}
	items := []item{}
	for rows.Next() {
		var v item
		if err := rows.Scan(&v.id, &v.title, &v.clientID, &v.acceptorID, &v.assigneeID); err != nil {
			rows.Close()
			return 0, err
		}
		items = append(items, v)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	n := 0
	for _, v := range items {
		title := "内部工单即将超时"
		body := fmt.Sprintf("内部工单 #%d「%s」剩余处理时间不足 15%%，请及时处理。", v.id, v.title)
		target := v.acceptorID
		if target == 0 {
			target = v.assigneeID
		}
		if target > 0 {
			_ = s.InsertNotification(ctx, target, "ticket_internal", title, body, "")
		}
		if v.clientID > 0 && v.clientID != target {
			_ = s.InsertNotification(ctx, v.clientID, "ticket_internal", title, body, "")
		}
		if _, err := s.DB.Exec(ctx, `UPDATE ticket_internal_tickets SET reminded_at=now() WHERE id=$1`, v.id); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}
