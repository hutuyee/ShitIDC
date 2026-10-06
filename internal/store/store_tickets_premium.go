package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/hutuyee/ShitIDC/internal/model"
)

// 用户工单升级（对齐魔方 CBAP TicketPremium 插件，045 迁移）：
// 部门 / 类型（处理时限）、自定义状态、预设回复、其他设置（接单后回复 /
// 仅跟进人回复 / 用户列表工单通知）、内部备注、操作日志、接单（领取人）、
// 关联产品、处理完成与用户评分（满意度 / 态度 / 时效）、催单、回复附件。

// 工单侧错误：API 层直接透出中文文案。
var (
	ErrTicketTypeInUse     = errors.New("工单类型已被工单引用，无法删除")
	ErrTicketDeptInUse     = errors.New("部门已被工单引用，无法删除")
	ErrTicketStatusInUse   = errors.New("工单状态正在使用中，无法删除")
	ErrTicketSystemStatus  = errors.New("系统状态不能删除")
	ErrTicketRateLimited   = errors.New("催单过于频繁，请稍后再试")
	ErrTicketAlreadyTaken  = errors.New("工单已被领取")
	ErrTicketReceiveFirst  = errors.New("请先接单后再回复")
	ErrTicketFollowOnly    = errors.New("仅工单领取人可以回复")
	ErrTicketNotFinished   = errors.New("工单还没有处理完成")
	ErrTicketAlreadyScored = errors.New("工单已经评过分了")
	ErrTicketScoreRange    = errors.New("每项评分需要在 0.5 ~ 5 分之间")
)

func ticketNullableID(id int64) any {
	if id <= 0 {
		return nil
	}
	return id
}

func ticketPremiumRandKey() string {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("s%d", time.Now().UnixNano())
	}
	return "s" + hex.EncodeToString(b)
}

// ticketIDByPublic 解析工单公开 ID；不存在时返回 ErrNotFound。
func (s *Store) ticketIDByPublic(ctx context.Context, publicID string) (int64, error) {
	var id int64
	err := s.DB.QueryRow(ctx, `SELECT id FROM tickets WHERE public_id=$1`, publicID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrNotFound
	}
	return id, err
}

// addTicketLog 写工单操作日志（尽力而为，失败不影响主流程）。
func (s *Store) addTicketLog(ctx context.Context, ticketID, adminID int64, action, description string) error {
	_, err := s.DB.Exec(ctx, `INSERT INTO ticket_logs(ticket_id,admin_id,action,description) VALUES($1,$2,$3,$4)`,
		ticketID, ticketNullableID(adminID), action, description)
	return err
}

// ---- 部门与类型 ----

// TicketDepartmentType 是部门下的工单类型（处理时限单位：小时）。
type TicketDepartmentType struct {
	ID              int64   `json:"id"`
	Name            string  `json:"name"`
	ProcessingLimit float64 `json:"processing_limit"`
}

// TicketDepartment 是工单部门（管理人员 / 主管 / 类型）。
type TicketDepartment struct {
	ID              int64                  `json:"id"`
	Name            string                 `json:"name"`
	AdminIDs        []int64                `json:"admin_ids"`
	Admins          []TicketInternalStaff  `json:"admin"`
	DirectorAdminID int64                  `json:"director_admin_id"`
	DirectorName    string                 `json:"director_admin_name"`
	Types           []TicketDepartmentType `json:"type"`
}

// TicketDepartmentInput 是部门新建 / 编辑入参。
type TicketDepartmentInput struct {
	Name            string
	AdminIDs        []int64
	DirectorAdminID int64
	Types           []TicketDepartmentType
}

// ticketStaffNames 批量解析员工显示名（昵称优先，回退邮箱）。
func (s *Store) ticketStaffNames(ctx context.Context, ids []int64) (map[int64]TicketInternalStaff, error) {
	out := map[int64]TicketInternalStaff{}
	uniq := make([]int64, 0, len(ids))
	seen := map[int64]bool{}
	for _, id := range ids {
		if id > 0 && !seen[id] {
			seen[id] = true
			uniq = append(uniq, id)
		}
	}
	if len(uniq) == 0 {
		return out, nil
	}
	rows, err := s.DB.Query(ctx, `SELECT u.id, coalesce(nullif(p.nickname,''),u.email), u.email
FROM users u LEFT JOIN user_profiles p ON p.user_id=u.id
WHERE u.id = ANY($1::bigint[])`, uniq)
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

// ListTicketDepartments 返回全部部门（含类型与人员显示名）。
func (s *Store) ListTicketDepartments(ctx context.Context) ([]TicketDepartment, error) {
	rows, err := s.DB.Query(ctx, `SELECT id,name,admin_ids,coalesce(director_admin_id,0) FROM ticket_departments ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TicketDepartment{}
	ids := []int64{}
	for rows.Next() {
		var d TicketDepartment
		if err := rows.Scan(&d.ID, &d.Name, &d.AdminIDs, &d.DirectorAdminID); err != nil {
			return nil, err
		}
		if d.AdminIDs == nil {
			d.AdminIDs = []int64{}
		}
		d.Admins = []TicketInternalStaff{}
		d.Types = []TicketDepartmentType{}
		out = append(out, d)
		ids = append(ids, d.AdminIDs...)
		ids = append(ids, d.DirectorAdminID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	names, err := s.ticketStaffNames(ctx, ids)
	if err != nil {
		return nil, err
	}
	trows, err := s.DB.Query(ctx, `SELECT id,department_id,name,processing_limit::float8 FROM ticket_types ORDER BY department_id,sort,id`)
	if err != nil {
		return nil, err
	}
	defer trows.Close()
	index := map[int64]int{}
	for i := range out {
		index[out[i].ID] = i
	}
	for trows.Next() {
		var t TicketDepartmentType
		var dept int64
		if err := trows.Scan(&t.ID, &dept, &t.Name, &t.ProcessingLimit); err != nil {
			return nil, err
		}
		if i, ok := index[dept]; ok {
			out[i].Types = append(out[i].Types, t)
		}
	}
	if err := trows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		d := &out[i]
		for _, id := range d.AdminIDs {
			if st, ok := names[id]; ok {
				d.Admins = append(d.Admins, st)
			}
		}
		if st, ok := names[d.DirectorAdminID]; ok {
			d.DirectorName = st.Name
		}
	}
	return out, nil
}

// CreateTicketDepartment 新建部门（连同类型）。
func (s *Store) CreateTicketDepartment(ctx context.Context, in TicketDepartmentInput) (int64, error) {
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" {
		return 0, errors.New("部门名称不能为空")
	}
	if len(in.AdminIDs) == 0 {
		return 0, errors.New("请选择部门管理人员")
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	var id int64
	if err := tx.QueryRow(ctx, `INSERT INTO ticket_departments(name,admin_ids,director_admin_id) VALUES($1,$2,$3) RETURNING id`,
		in.Name, in.AdminIDs, ticketNullableID(in.DirectorAdminID)).Scan(&id); err != nil {
		return 0, err
	}
	for i, t := range in.Types {
		name := strings.TrimSpace(t.Name)
		if name == "" {
			continue
		}
		if _, err := tx.Exec(ctx, `INSERT INTO ticket_types(department_id,name,processing_limit,sort) VALUES($1,$2,$3,$4)`,
			id, name, t.ProcessingLimit, i); err != nil {
			return 0, err
		}
	}
	return id, tx.Commit(ctx)
}

// UpdateTicketDepartment 编辑部门：名称 / 人员 / 主管替换，类型按 id 增量更新；
// 已被工单引用的类型不允许删除。
func (s *Store) UpdateTicketDepartment(ctx context.Context, id int64, in TicketDepartmentInput) error {
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" {
		return errors.New("部门名称不能为空")
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `UPDATE ticket_departments SET name=$2,admin_ids=$3,director_admin_id=$4,updated_at=now() WHERE id=$1`,
		id, in.Name, in.AdminIDs, ticketNullableID(in.DirectorAdminID))
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	existing := map[int64]bool{}
	rows, err := tx.Query(ctx, `SELECT id FROM ticket_types WHERE department_id=$1`, id)
	if err != nil {
		return err
	}
	for rows.Next() {
		var tid int64
		if err := rows.Scan(&tid); err != nil {
			rows.Close()
			return err
		}
		existing[tid] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	keep := map[int64]bool{}
	for i, t := range in.Types {
		name := strings.TrimSpace(t.Name)
		if name == "" {
			continue
		}
		if t.ID > 0 && existing[t.ID] {
			if _, err := tx.Exec(ctx, `UPDATE ticket_types SET name=$2,processing_limit=$3,sort=$4 WHERE id=$1 AND department_id=$5`,
				t.ID, name, t.ProcessingLimit, i, id); err != nil {
				return err
			}
			keep[t.ID] = true
			continue
		}
		if _, err := tx.Exec(ctx, `INSERT INTO ticket_types(department_id,name,processing_limit,sort) VALUES($1,$2,$3,$4)`,
			id, name, t.ProcessingLimit, i); err != nil {
			return err
		}
	}
	for tid := range existing {
		if keep[tid] {
			continue
		}
		var n int64
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM tickets WHERE ticket_type_id=$1`, tid).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return ErrTicketTypeInUse
		}
		if _, err := tx.Exec(ctx, `DELETE FROM ticket_types WHERE id=$1`, tid); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// DeleteTicketDepartment 删除部门；已被工单引用的部门拒绝删除。
func (s *Store) DeleteTicketDepartment(ctx context.Context, id int64) error {
	var n int64
	if err := s.DB.QueryRow(ctx, `SELECT count(*) FROM tickets WHERE department_id=$1`, id).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return ErrTicketDeptInUse
	}
	tag, err := s.DB.Exec(ctx, `DELETE FROM ticket_departments WHERE id=$1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ---- 工单状态 ----

// TicketStatus 是自定义工单状态；key 为工单 status 列存储值。
type TicketStatus struct {
	ID       int64  `json:"id"`
	Key      string `json:"key"`
	Name     string `json:"name"`
	Color    string `json:"color"`
	Finished bool   `json:"finished"`
	System   bool   `json:"system"`
	Sort     int    `json:"sort"`
}

// ListTicketStatuses 返回全部状态。
func (s *Store) ListTicketStatuses(ctx context.Context) ([]TicketStatus, error) {
	rows, err := s.DB.Query(ctx, `SELECT id,key,name,color,finished,system,sort FROM ticket_statuses ORDER BY sort,id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TicketStatus{}
	for rows.Next() {
		var v TicketStatus
		if err := rows.Scan(&v.ID, &v.Key, &v.Name, &v.Color, &v.Finished, &v.System, &v.Sort); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// CreateTicketStatus 新增状态（key 自动生成）。
func (s *Store) CreateTicketStatus(ctx context.Context, name, color string, finished bool) (int64, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return 0, errors.New("状态名称不能为空")
	}
	if strings.TrimSpace(color) == "" {
		color = "#909399"
	}
	var id int64
	err := s.DB.QueryRow(ctx, `INSERT INTO ticket_statuses(key,name,color,finished) VALUES($1,$2,$3,$4) RETURNING id`,
		ticketPremiumRandKey(), name, color, finished).Scan(&id)
	return id, err
}

// UpdateTicketStatus 编辑状态（名称 / 颜色 / 完结标记）。
func (s *Store) UpdateTicketStatus(ctx context.Context, id int64, name, color string, finished bool) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("状态名称不能为空")
	}
	if strings.TrimSpace(color) == "" {
		color = "#909399"
	}
	tag, err := s.DB.Exec(ctx, `UPDATE ticket_statuses SET name=$2,color=$3,finished=$4,updated_at=now() WHERE id=$1`,
		id, name, color, finished)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteTicketStatus 删除状态；系统状态与使用中的状态拒绝删除。
func (s *Store) DeleteTicketStatus(ctx context.Context, id int64) error {
	var key string
	var system bool
	err := s.DB.QueryRow(ctx, `SELECT key,system FROM ticket_statuses WHERE id=$1`, id).Scan(&key, &system)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if system {
		return ErrTicketSystemStatus
	}
	var n int64
	if err := s.DB.QueryRow(ctx, `SELECT count(*) FROM tickets WHERE status=$1`, key).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return ErrTicketStatusInUse
	}
	_, err = s.DB.Exec(ctx, `DELETE FROM ticket_statuses WHERE id=$1`, id)
	return err
}

func (s *Store) ticketStatusExists(ctx context.Context, key string) (string, error) {
	var name string
	err := s.DB.QueryRow(ctx, `SELECT name FROM ticket_statuses WHERE key=$1`, key).Scan(&name)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	return name, err
}

// ---- 预设回复 ----

// TicketPrereply 是客服预设回复。
type TicketPrereply struct {
	ID        int64     `json:"id"`
	Content   string    `json:"content"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// ListTicketPrereplies 返回全部预设回复。
func (s *Store) ListTicketPrereplies(ctx context.Context) ([]TicketPrereply, error) {
	rows, err := s.DB.Query(ctx, `SELECT id,content,created_at,updated_at FROM ticket_prereplies ORDER BY id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TicketPrereply{}
	for rows.Next() {
		var v TicketPrereply
		if err := rows.Scan(&v.ID, &v.Content, &v.CreatedAt, &v.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// CreateTicketPrereply 新增预设回复。
func (s *Store) CreateTicketPrereply(ctx context.Context, content string) (int64, error) {
	content = strings.TrimSpace(content)
	if content == "" {
		return 0, errors.New("预设回复内容不能为空")
	}
	var id int64
	err := s.DB.QueryRow(ctx, `INSERT INTO ticket_prereplies(content) VALUES($1) RETURNING id`, content).Scan(&id)
	return id, err
}

// UpdateTicketPrereply 编辑预设回复。
func (s *Store) UpdateTicketPrereply(ctx context.Context, id int64, content string) error {
	content = strings.TrimSpace(content)
	if content == "" {
		return errors.New("预设回复内容不能为空")
	}
	tag, err := s.DB.Exec(ctx, `UPDATE ticket_prereplies SET content=$2,updated_at=now() WHERE id=$1`, id, content)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteTicketPrereply 删除预设回复。
func (s *Store) DeleteTicketPrereply(ctx context.Context, id int64) error {
	tag, err := s.DB.Exec(ctx, `DELETE FROM ticket_prereplies WHERE id=$1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ---- 其他设置 ----

// TicketConfig 是工单「其他设置」。
type TicketConfig struct {
	RefreshTime             string `json:"refresh_time"`
	TicketReceiveReply      string `json:"ticket_receive_reply"`
	TicketFollowReply       string `json:"ticket_follow_reply"`
	TicketNoticeOpen        string `json:"ticket_notice_open"`
	TicketNoticeDescription string `json:"ticket_notice_description"`
}

// GetTicketConfig 读取工单配置。
func (s *Store) GetTicketConfig(ctx context.Context) (TicketConfig, error) {
	cfg := TicketConfig{RefreshTime: "180", TicketReceiveReply: "0", TicketFollowReply: "0", TicketNoticeOpen: "0"}
	rows, err := s.DB.Query(ctx, `SELECT k,v FROM ticket_config`)
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
		case "refresh_time":
			cfg.RefreshTime = v
		case "ticket_receive_reply":
			cfg.TicketReceiveReply = v
		case "ticket_follow_reply":
			cfg.TicketFollowReply = v
		case "ticket_notice_open":
			cfg.TicketNoticeOpen = v
		case "ticket_notice_description":
			cfg.TicketNoticeDescription = v
		}
	}
	return cfg, rows.Err()
}

// SaveTicketConfig 保存工单配置。
func (s *Store) SaveTicketConfig(ctx context.Context, cfg TicketConfig) error {
	items := map[string]string{
		"refresh_time":              cfg.RefreshTime,
		"ticket_receive_reply":      cfg.TicketReceiveReply,
		"ticket_follow_reply":       cfg.TicketFollowReply,
		"ticket_notice_open":        cfg.TicketNoticeOpen,
		"ticket_notice_description": cfg.TicketNoticeDescription,
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	for k, v := range items {
		if _, err := tx.Exec(ctx, `INSERT INTO ticket_config(k,v) VALUES($1,$2)
ON CONFLICT (k) DO UPDATE SET v=excluded.v`, k, strings.TrimSpace(v)); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// ---- 内部备注 ----

// TicketNote 是工单内部备注（用户不可见）。
type TicketNote struct {
	ID        int64     `json:"id"`
	AdminID   int64     `json:"admin_id"`
	AdminName string    `json:"admin_name"`
	Content   string    `json:"content"`
	CreatedAt time.Time `json:"create_time"`
}

// ListTicketNotes 返回工单备注。
func (s *Store) ListTicketNotes(ctx context.Context, ticketPublicID string) ([]TicketNote, error) {
	rows, err := s.DB.Query(ctx, `SELECT n.id,coalesce(n.admin_id,0),
(SELECT coalesce(nullif(p.nickname,''),u.email,'') FROM users u LEFT JOIN user_profiles p ON p.user_id=u.id WHERE u.id=n.admin_id),
n.content,n.created_at
FROM ticket_notes n WHERE n.ticket_id=(SELECT id FROM tickets WHERE public_id=$1) ORDER BY n.created_at ASC,n.id ASC`, ticketPublicID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TicketNote{}
	for rows.Next() {
		var v TicketNote
		if err := rows.Scan(&v.ID, &v.AdminID, &v.AdminName, &v.Content, &v.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// AddTicketNote 新增备注。
func (s *Store) AddTicketNote(ctx context.Context, ticketPublicID string, adminID int64, content string) (TicketNote, error) {
	content = strings.TrimSpace(content)
	if content == "" {
		return TicketNote{}, errors.New("备注内容不能为空")
	}
	ticketID, err := s.ticketIDByPublic(ctx, ticketPublicID)
	if err != nil {
		return TicketNote{}, err
	}
	var v TicketNote
	err = s.DB.QueryRow(ctx, `INSERT INTO ticket_notes(ticket_id,admin_id,content) VALUES($1,$2,$3)
RETURNING id,coalesce(admin_id,0),content,created_at`, ticketID, adminID, content).Scan(&v.ID, &v.AdminID, &v.Content, &v.CreatedAt)
	if err != nil {
		return TicketNote{}, err
	}
	names, _ := s.ticketStaffNames(ctx, []int64{adminID})
	v.AdminName = names[adminID].Name
	_ = s.addTicketLog(ctx, ticketID, adminID, "note_add", "添加内部备注")
	return v, nil
}

// UpdateTicketNote 编辑备注。
func (s *Store) UpdateTicketNote(ctx context.Context, id, adminID int64, content string) error {
	content = strings.TrimSpace(content)
	if content == "" {
		return errors.New("备注内容不能为空")
	}
	var ticketID int64
	err := s.DB.QueryRow(ctx, `UPDATE ticket_notes SET content=$2,updated_at=now() WHERE id=$1 RETURNING ticket_id`, id, content).Scan(&ticketID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	_ = s.addTicketLog(ctx, ticketID, adminID, "note_update", "修改内部备注")
	return nil
}

// DeleteTicketNote 删除备注。
func (s *Store) DeleteTicketNote(ctx context.Context, id, adminID int64) error {
	var ticketID int64
	err := s.DB.QueryRow(ctx, `DELETE FROM ticket_notes WHERE id=$1 RETURNING ticket_id`, id).Scan(&ticketID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	_ = s.addTicketLog(ctx, ticketID, adminID, "note_delete", "删除内部备注")
	return nil
}

// ---- 操作日志 ----

// TicketLog 是工单操作日志。
type TicketLog struct {
	ID          int64     `json:"id"`
	AdminID     int64     `json:"admin_id"`
	AdminName   string    `json:"admin_name"`
	Action      string    `json:"action"`
	Description string    `json:"description"`
	CreatedAt   time.Time `json:"create_time"`
}

// ListTicketLogs 返回工单日志（倒序分页）。
func (s *Store) ListTicketLogs(ctx context.Context, ticketPublicID string, page, limit int) ([]TicketLog, int64, error) {
	if limit <= 0 || limit > 200 {
		limit = 20
	}
	if page < 1 {
		page = 1
	}
	var total int64
	if err := s.DB.QueryRow(ctx, `SELECT count(*) FROM ticket_logs WHERE ticket_id=(SELECT id FROM tickets WHERE public_id=$1)`, ticketPublicID).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.DB.Query(ctx, `SELECT l.id,coalesce(l.admin_id,0),
(SELECT coalesce(nullif(p.nickname,''),u.email,'') FROM users u LEFT JOIN user_profiles p ON p.user_id=u.id WHERE u.id=l.admin_id),
l.action,l.description,l.created_at
FROM ticket_logs l WHERE l.ticket_id=(SELECT id FROM tickets WHERE public_id=$1)
ORDER BY l.created_at DESC,l.id DESC LIMIT $2 OFFSET $3`, ticketPublicID, limit, (page-1)*limit)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []TicketLog{}
	for rows.Next() {
		var v TicketLog
		if err := rows.Scan(&v.ID, &v.AdminID, &v.AdminName, &v.Action, &v.Description, &v.CreatedAt); err != nil {
			return nil, 0, err
		}
		out = append(out, v)
	}
	return out, total, rows.Err()
}

// ---- 客服人员 ----

// ListTicketStaff 返回拥有客服工单权限的管理人员（部门人员选择器用）。
func (s *Store) ListTicketStaff(ctx context.Context) ([]TicketInternalStaff, error) {
	rows, err := s.DB.Query(ctx, `SELECT u.id, coalesce(nullif(p.nickname,''),u.email), u.email
FROM users u
LEFT JOIN user_profiles p ON p.user_id=u.id
WHERE u.deleted_at IS NULL AND u.status='active' AND EXISTS (
  SELECT 1 FROM user_roles ur
  JOIN role_permissions rp ON rp.role_id=ur.role_id
  JOIN permissions pe ON pe.id=rp.permission_id
  WHERE ur.user_id=u.id AND pe.name='ticket.manage')
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

// ---- 用户工单：列表 ----

const ticketPremiumListSelect = `SELECT t.public_id::text, coalesce(t.number,''), t.subject, t.status,
coalesce(st.name,''), coalesce(st.color,'#909399'), t.finished, t.priority,
t.user_id, u.email,
coalesce(t.department_id,0), coalesce(dp.name,''),
coalesce(t.ticket_type_id,0), coalesce(tt.name,''), coalesce(tt.processing_limit,0)::float8,
coalesce(t.admin_id,0),
(SELECT coalesce(nullif(pf.nickname,''),u2.email,'') FROM users u2 LEFT JOIN user_profiles pf ON pf.user_id=u2.id WHERE u2.id=t.admin_id),
coalesce(t.last_reply_admin_id,0),
(SELECT coalesce(nullif(pf.nickname,''),u2.email,'') FROM users u2 LEFT JOIN user_profiles pf ON pf.user_id=u2.id WHERE u2.id=t.last_reply_admin_id),
t.last_reply_at, t.created_at, t.is_score, t.last_urge_time
FROM tickets t
JOIN users u ON u.id=t.user_id
LEFT JOIN ticket_statuses st ON st.key=t.status
LEFT JOIN ticket_departments dp ON dp.id=t.department_id
LEFT JOIN ticket_types tt ON tt.id=t.ticket_type_id`

// TicketPremiumFilter 是工单列表筛选（用户侧只填 ClientID）。
type TicketPremiumFilter struct {
	Keywords         string
	TypeIDs          []int64
	Statuses         []string
	ClientID         int64
	LastReplyAdminID int64
	AdminID          int64
	Start            *time.Time
	End              *time.Time
	Page             int
	Limit            int
}

// ListTicketsPremium 返回工单列表与总数（按处理中优先、最近活动倒序）。
func (s *Store) ListTicketsPremium(ctx context.Context, f TicketPremiumFilter) ([]model.Ticket, int64, error) {
	where := []string{"TRUE"}
	args := []any{}
	add := func(v any) string {
		args = append(args, v)
		return fmt.Sprintf("$%d", len(args))
	}
	if kw := strings.TrimSpace(f.Keywords); kw != "" {
		p := add("%" + kw + "%")
		where = append(where, "(t.subject ILIKE "+p+" OR coalesce(t.number,'') ILIKE "+p+")")
	}
	if len(f.TypeIDs) > 0 {
		where = append(where, "t.ticket_type_id = ANY("+add(f.TypeIDs)+"::bigint[])")
	}
	if len(f.Statuses) > 0 {
		where = append(where, "t.status = ANY("+add(f.Statuses)+"::text[])")
	}
	if f.ClientID > 0 {
		where = append(where, "t.user_id = "+add(f.ClientID))
	}
	if f.LastReplyAdminID > 0 {
		where = append(where, "t.last_reply_admin_id = "+add(f.LastReplyAdminID))
	}
	if f.AdminID > 0 {
		where = append(where, "t.admin_id = "+add(f.AdminID))
	}
	if f.Start != nil {
		where = append(where, "t.created_at >= "+add(*f.Start))
	}
	if f.End != nil {
		where = append(where, "t.created_at <= "+add(*f.End))
	}
	clause := strings.Join(where, " AND ")
	var total int64
	if err := s.DB.QueryRow(ctx, "SELECT count(*) FROM tickets t WHERE "+clause, args...).Scan(&total); err != nil {
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
	q := ticketPremiumListSelect + "\nWHERE " + clause + fmt.Sprintf("\nORDER BY t.finished ASC, COALESCE(t.last_reply_at,t.created_at) DESC, t.id DESC LIMIT %d OFFSET %d", limit, (page-1)*limit)
	rows, err := s.DB.Query(ctx, q, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []model.Ticket{}
	for rows.Next() {
		var t model.Ticket
		var adminID, lastAdminID int64
		var adminName, lastAdminName string
		var limitHours float64
		if err := rows.Scan(&t.PublicID, &t.Number, &t.Subject, &t.Status, &t.StatusName, &t.StatusColor, &t.Finished, &t.Priority,
			&t.UserUID, &t.UserEmail, &t.DepartmentID, &t.DepartmentName, &t.TicketTypeID, &t.TypeName, &limitHours,
			&adminID, &adminName, &lastAdminID, &lastAdminName, &t.LastReplyAt, &t.CreatedAt, &t.IsScore, &t.LastUrgeTime); err != nil {
			return nil, 0, err
		}
		t.AdminUID, t.AdminName = adminID, adminName
		t.LastReplyAdminUID, t.LastReplyAdminName = lastAdminID, lastAdminName
		if limitHours > 0 {
			due := t.CreatedAt.Add(time.Duration(limitHours * float64(time.Hour)))
			t.DueTime = &due
		}
		out = append(out, t)
	}
	return out, total, rows.Err()
}

// ---- 用户工单：详情 ----

// TicketPremiumDetail 是工单详情（含会话、备注、附件、关联产品）。
type TicketPremiumDetail struct {
	model.Ticket
	Messages    []model.TicketMessage    `json:"messages"`
	Notes       []TicketNote             `json:"notes"`
	Attachments []model.TicketAttachment `json:"attachments"`
	Hosts       []TicketInternalHost     `json:"hosts"`
}

// ticketHostsByIDs 返回关联产品（内部 ID 列表 → 展示信息）。
func (s *Store) ticketHostsByIDs(ctx context.Context, ids []int64) ([]TicketInternalHost, error) {
	out := []TicketInternalHost{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := s.DB.Query(ctx, `SELECT s.id, s.public_id::text, p.name, s.status
FROM services s JOIN products p ON p.id=s.product_id
WHERE s.id = ANY($1::bigint[]) ORDER BY s.id`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
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

// GetTicketPremium 读取工单详情；userID > 0 时校验归属且不返回内部备注。
func (s *Store) GetTicketPremium(ctx context.Context, publicID string, userID int64) (TicketPremiumDetail, error) {
	var d TicketPremiumDetail
	var ownerID, adminID, lastAdminID, postAdminID int64
	var adminName, lastAdminName, postAdminName string
	var limitHours float64
	var hostIDs []int64
	var attachmentIDs []string
	err := s.DB.QueryRow(ctx, `SELECT t.public_id::text, coalesce(t.number,''), t.subject, t.status,
coalesce(st.name,''), coalesce(st.color,'#909399'), t.finished, t.priority,
u.id, u.email,
coalesce(t.department_id,0), coalesce(dp.name,''),
coalesce(t.ticket_type_id,0), coalesce(tt.name,''), coalesce(tt.processing_limit,0)::float8,
coalesce(t.admin_id,0),
(SELECT coalesce(nullif(pf.nickname,''),u2.email,'') FROM users u2 LEFT JOIN user_profiles pf ON pf.user_id=u2.id WHERE u2.id=t.admin_id),
coalesce(t.last_reply_admin_id,0),
(SELECT coalesce(nullif(pf.nickname,''),u2.email,'') FROM users u2 LEFT JOIN user_profiles pf ON pf.user_id=u2.id WHERE u2.id=t.last_reply_admin_id),
coalesce(t.post_admin_id,0),
(SELECT coalesce(nullif(pf.nickname,''),u2.email,'') FROM users u2 LEFT JOIN user_profiles pf ON pf.user_id=u2.id WHERE u2.id=t.post_admin_id),
t.host_ids, coalesce(t.attachment,'{}'),
t.finish_time, t.is_score, coalesce(t.satisfaction,0)::float8, coalesce(t.attitude,0)::float8, coalesce(t.processing_time,0)::float8, t.score_time,
t.last_urge_time, t.urge_count, t.last_reply_at, t.last_reply_is_staff, t.created_at
FROM tickets t JOIN users u ON u.id=t.user_id
LEFT JOIN ticket_statuses st ON st.key=t.status
LEFT JOIN ticket_departments dp ON dp.id=t.department_id
LEFT JOIN ticket_types tt ON tt.id=t.ticket_type_id
WHERE t.public_id=$1`, publicID).Scan(&d.PublicID, &d.Number, &d.Subject, &d.Status, &d.StatusName, &d.StatusColor, &d.Finished, &d.Priority,
		&ownerID, &d.UserEmail, &d.DepartmentID, &d.DepartmentName, &d.TicketTypeID, &d.TypeName, &limitHours,
		&adminID, &adminName, &lastAdminID, &lastAdminName, &postAdminID, &postAdminName,
		&hostIDs, &attachmentIDs,
		&d.FinishTime, &d.IsScore, &d.Satisfaction, &d.Attitude, &d.ScoreProcessing, &d.ScoreTime,
		&d.LastUrgeTime, &d.UrgeCount, &d.LastReplyAt, &d.LastReplyIsStaff, &d.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return TicketPremiumDetail{}, ErrNotFound
	}
	if err != nil {
		return TicketPremiumDetail{}, err
	}
	if userID > 0 && ownerID != userID {
		return TicketPremiumDetail{}, ErrNotFound
	}
	d.UserUID = ownerID
	d.AdminUID, d.AdminName = adminID, adminName
	d.LastReplyAdminUID, d.LastReplyAdminName = lastAdminID, lastAdminName
	d.PostAdminUID, d.PostAdminName = postAdminID, postAdminName
	d.HostIDs = hostIDs
	if d.HostIDs == nil {
		d.HostIDs = []int64{}
	}
	d.AttachmentIDs = attachmentIDs
	if limitHours > 0 {
		due := d.CreatedAt.Add(time.Duration(limitHours * float64(time.Hour)))
		d.DueTime = &due
	}
	mrows, err := s.DB.Query(ctx, `SELECT m.id::text, coalesce(m.user_id,0),
(SELECT coalesce(nullif(pf.nickname,''),u.email,'') FROM users u LEFT JOIN user_profiles pf ON pf.user_id=u.id WHERE u.id=m.user_id),
coalesce((SELECT email FROM users u WHERE u.id=m.user_id),''), m.is_staff, m.body, coalesce(m.attachment,'{}'), m.created_at
FROM ticket_messages m
WHERE m.ticket_id=(SELECT id FROM tickets WHERE public_id=$1) ORDER BY m.created_at ASC, m.id ASC`, publicID)
	if err != nil {
		return TicketPremiumDetail{}, err
	}
	defer mrows.Close()
	d.Messages = []model.TicketMessage{}
	for mrows.Next() {
		var m model.TicketMessage
		if err := mrows.Scan(&m.PublicID, &m.SenderUID, &m.SenderName, &m.SenderEmail, &m.IsStaff, &m.Body, &m.Attachment, &m.CreatedAt); err != nil {
			return TicketPremiumDetail{}, err
		}
		if m.Attachment == nil {
			m.Attachment = []string{}
		}
		d.Messages = append(d.Messages, m)
	}
	if err := mrows.Err(); err != nil {
		return TicketPremiumDetail{}, err
	}
	d.Notes = []TicketNote{}
	if userID <= 0 {
		notes, err := s.ListTicketNotes(ctx, publicID)
		if err != nil {
			return TicketPremiumDetail{}, err
		}
		d.Notes = notes
	}
	atts, err := s.ListTicketAttachments(ctx, publicID)
	if err != nil {
		return TicketPremiumDetail{}, err
	}
	d.Attachments = atts
	hosts, err := s.ticketHostsByIDs(ctx, d.HostIDs)
	if err != nil {
		return TicketPremiumDetail{}, err
	}
	d.Hosts = hosts
	return d, nil
}

// ---- 用户工单：创建 / 回复 ----

// TicketPremiumCreateInput 是新建工单入参；PostAdminID > 0 表示客服代用户建单。
type TicketPremiumCreateInput struct {
	UserID        int64
	PostAdminID   int64
	DepartmentID  int64
	TypeID        int64
	Title         string
	Priority      string
	HostIDs       []int64
	Message       string
	AttachmentIDs []string
}

// CreateTicketPremium 新建工单（含首条消息与编号）。
func (s *Store) CreateTicketPremium(ctx context.Context, in TicketPremiumCreateInput) (model.Ticket, error) {
	in.Title = strings.TrimSpace(in.Title)
	in.Message = strings.TrimSpace(in.Message)
	if in.Title == "" {
		return model.Ticket{}, errors.New("工单标题不能为空")
	}
	if len([]rune(in.Title)) > 100 {
		return model.Ticket{}, errors.New("工单标题不能超过 100 字")
	}
	if in.Message == "" {
		return model.Ticket{}, errors.New("工单内容不能为空")
	}
	if in.Priority == "" {
		in.Priority = "normal"
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return model.Ticket{}, err
	}
	defer tx.Rollback(ctx)
	if in.TypeID > 0 {
		var deptID int64
		err := tx.QueryRow(ctx, `SELECT department_id FROM ticket_types WHERE id=$1`, in.TypeID).Scan(&deptID)
		if errors.Is(err, pgx.ErrNoRows) {
			return model.Ticket{}, errors.New("工单类型不存在")
		}
		if err != nil {
			return model.Ticket{}, err
		}
		if in.DepartmentID > 0 && in.DepartmentID != deptID {
			return model.Ticket{}, errors.New("工单类型与部门不匹配")
		}
		in.DepartmentID = deptID
	} else if in.DepartmentID > 0 {
		var n int64
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM ticket_departments WHERE id=$1`, in.DepartmentID).Scan(&n); err != nil {
			return model.Ticket{}, err
		}
		if n == 0 {
			return model.Ticket{}, errors.New("工单部门不存在")
		}
	}
	hosts := in.HostIDs
	if hosts == nil {
		hosts = []int64{}
	}
	atts := in.AttachmentIDs
	if atts == nil {
		atts = []string{}
	}
	authorID := in.UserID
	isStaff := false
	if in.PostAdminID > 0 {
		authorID = in.PostAdminID
		isStaff = true
	}
	var t model.Ticket
	var ticketID int64
	if err := tx.QueryRow(ctx, `INSERT INTO tickets(user_id,subject,priority,department_id,ticket_type_id,host_ids,attachment,last_reply_at,last_reply_is_staff,post_admin_id)
VALUES($1,$2,$3,$4,$5,$6,$7,now(),$8,$9)
RETURNING id,public_id::text,subject,status,priority,created_at`,
		in.UserID, in.Title, in.Priority, ticketNullableID(in.DepartmentID), ticketNullableID(in.TypeID), hosts, atts, isStaff, ticketNullableID(in.PostAdminID)).
		Scan(&ticketID, &t.PublicID, &t.Subject, &t.Status, &t.Priority, &t.CreatedAt); err != nil {
		return model.Ticket{}, err
	}
	t.Number = fmt.Sprintf("T%s%05d", t.CreatedAt.Format("20060102"), ticketID)
	if _, err := tx.Exec(ctx, `UPDATE tickets SET number=$2 WHERE id=$1`, ticketID, t.Number); err != nil {
		return model.Ticket{}, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO ticket_messages(ticket_id,user_id,is_staff,body,attachment) VALUES($1,$2,$3,$4,$5)`,
		ticketID, authorID, isStaff, in.Message, atts); err != nil {
		return model.Ticket{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return model.Ticket{}, err
	}
	if in.PostAdminID > 0 {
		_ = s.addTicketLog(ctx, ticketID, in.PostAdminID, "create", "新建工单")
	}
	return t, nil
}

// ReplyTicketPremium 追加回复（可带附件）；客服回复进入待回复，用户回复回到待处理。
func (s *Store) ReplyTicketPremium(ctx context.Context, ticketPublicID string, senderID int64, isStaff bool, body string, attachmentIDs []string) (model.TicketMessage, int64, error) {
	body = strings.TrimSpace(body)
	if body == "" {
		return model.TicketMessage{}, 0, errors.New("回复内容不能为空")
	}
	if attachmentIDs == nil {
		attachmentIDs = []string{}
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return model.TicketMessage{}, 0, err
	}
	defer tx.Rollback(ctx)
	var ticketID, ownerID int64
	var status string
	err = tx.QueryRow(ctx, `SELECT id,user_id,status FROM tickets WHERE public_id=$1 FOR UPDATE`, ticketPublicID).Scan(&ticketID, &ownerID, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.TicketMessage{}, 0, ErrNotFound
	}
	if err != nil {
		return model.TicketMessage{}, 0, err
	}
	if !isStaff && ownerID != senderID {
		return model.TicketMessage{}, 0, ErrNotFound
	}
	newStatus := "open"
	if isStaff {
		newStatus = "pending"
	}
	var m model.TicketMessage
	m.SenderUID = senderID
	m.IsStaff = isStaff
	m.Body = body
	m.Attachment = attachmentIDs
	if err := tx.QueryRow(ctx, `INSERT INTO ticket_messages(ticket_id,user_id,is_staff,body,attachment) VALUES($1,$2,$3,$4,$5)
RETURNING id::text,created_at`, ticketID, senderID, isStaff, body, attachmentIDs).Scan(&m.PublicID, &m.CreatedAt); err != nil {
		return model.TicketMessage{}, 0, err
	}
	if isStaff {
		if _, err := tx.Exec(ctx, `UPDATE tickets SET status=$2,last_reply_at=now(),last_reply_is_staff=TRUE,last_reply_admin_id=$3,admin_id=coalesce(admin_id,$3),updated_at=now() WHERE id=$1`,
			ticketID, newStatus, senderID); err != nil {
			return model.TicketMessage{}, 0, err
		}
	} else {
		if _, err := tx.Exec(ctx, `UPDATE tickets SET status=$2,last_reply_at=now(),last_reply_is_staff=FALSE,updated_at=now() WHERE id=$1`,
			ticketID, newStatus); err != nil {
			return model.TicketMessage{}, 0, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return model.TicketMessage{}, 0, err
	}
	if names, err := s.ticketStaffNames(ctx, []int64{senderID}); err == nil {
		m.SenderName = names[senderID].Name
	}
	return m, ownerID, nil
}

// UpdateTicketReply 客服修改回复内容。
func (s *Store) UpdateTicketReply(ctx context.Context, replyID string, adminID int64, body string) error {
	body = strings.TrimSpace(body)
	if body == "" {
		return errors.New("回复内容不能为空")
	}
	id, err := strconv.ParseInt(strings.TrimSpace(replyID), 10, 64)
	if err != nil {
		return ErrNotFound
	}
	var ticketID int64
	err = s.DB.QueryRow(ctx, `UPDATE ticket_messages SET body=$2 WHERE id=$1 RETURNING ticket_id`, id, body).Scan(&ticketID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	_ = s.addTicketLog(ctx, ticketID, adminID, "reply_update", "修改回复")
	return nil
}

// DeleteTicketReply 客服删除回复。
func (s *Store) DeleteTicketReply(ctx context.Context, replyID string, adminID int64) error {
	id, err := strconv.ParseInt(strings.TrimSpace(replyID), 10, 64)
	if err != nil {
		return ErrNotFound
	}
	var ticketID int64
	err = s.DB.QueryRow(ctx, `DELETE FROM ticket_messages WHERE id=$1 RETURNING ticket_id`, id).Scan(&ticketID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	_ = s.addTicketLog(ctx, ticketID, adminID, "reply_delete", "删除回复")
	return nil
}

// ---- 用户工单：领取 / 处理 / 状态 / 保存 ----

// AcceptTicketPremium 领取工单（写领取人）。
func (s *Store) AcceptTicketPremium(ctx context.Context, ticketPublicID string, adminID int64) error {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var ticketID, current int64
	err = tx.QueryRow(ctx, `SELECT id,coalesce(admin_id,0) FROM tickets WHERE public_id=$1 FOR UPDATE`, ticketPublicID).Scan(&ticketID, &current)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if current != 0 && current != adminID {
		return ErrTicketAlreadyTaken
	}
	if _, err := tx.Exec(ctx, `UPDATE tickets SET admin_id=$2,updated_at=now() WHERE id=$1`, ticketID, adminID); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	_ = s.addTicketLog(ctx, ticketID, adminID, "accept", "接单")
	return nil
}

// FinishTicketPremium 处理完成（可选同时关闭）。
func (s *Store) FinishTicketPremium(ctx context.Context, ticketPublicID string, adminID int64, closeTicket bool) error {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var ticketID int64
	err = tx.QueryRow(ctx, `SELECT id FROM tickets WHERE public_id=$1 FOR UPDATE`, ticketPublicID).Scan(&ticketID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE tickets SET finished=TRUE,finish_time=now(),admin_id=coalesce(admin_id,$2),
status=CASE WHEN $3 THEN 'closed' ELSE status END,updated_at=now() WHERE id=$1`, ticketID, adminID, closeTicket); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	desc := "处理完成"
	if closeTicket {
		desc = "处理完成并关闭工单"
	}
	_ = s.addTicketLog(ctx, ticketID, adminID, "processed", desc)
	return nil
}

// SaveTicketPremiumFields 保存详情页的类型 / 状态 / 关联产品。
func (s *Store) SaveTicketPremiumFields(ctx context.Context, ticketPublicID string, adminID, typeID int64, statusKey string, hostIDs []int64) error {
	statusKey = strings.TrimSpace(statusKey)
	if statusKey != "" {
		if _, err := s.ticketStatusExists(ctx, statusKey); err != nil {
			return err
		}
	}
	deptID := int64(0)
	if typeID > 0 {
		if err := s.DB.QueryRow(ctx, `SELECT department_id FROM ticket_types WHERE id=$1`, typeID).Scan(&deptID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return errors.New("工单类型不存在")
			}
			return err
		}
	}
	if hostIDs == nil {
		hostIDs = []int64{}
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var ticketID int64
	err = tx.QueryRow(ctx, `SELECT id FROM tickets WHERE public_id=$1 FOR UPDATE`, ticketPublicID).Scan(&ticketID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE tickets SET ticket_type_id=$2,department_id=$3,host_ids=$4,
status=CASE WHEN $5='' THEN status ELSE $5 END,updated_at=now() WHERE id=$1`,
		ticketID, ticketNullableID(typeID), ticketNullableID(deptID), hostIDs, statusKey); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	_ = s.addTicketLog(ctx, ticketID, adminID, "save", "保存工单（类型 / 状态 / 关联产品）")
	return nil
}

// SetTicketPremiumStatus 客服关闭 / 重新打开工单。
func (s *Store) SetTicketPremiumStatus(ctx context.Context, ticketPublicID string, adminID int64, statusKey string) error {
	statusKey = strings.TrimSpace(statusKey)
	if _, err := s.ticketStatusExists(ctx, statusKey); err != nil {
		return err
	}
	ticketID, err := s.ticketIDByPublic(ctx, ticketPublicID)
	if err != nil {
		return err
	}
	if _, err := s.DB.Exec(ctx, `UPDATE tickets SET status=$2,updated_at=now() WHERE id=$1`, ticketID, statusKey); err != nil {
		return err
	}
	desc := "更新工单状态"
	switch statusKey {
	case "closed":
		desc = "关闭工单"
	case "open":
		desc = "重新打开工单"
	case "pending":
		desc = "标记为待回复"
	}
	_ = s.addTicketLog(ctx, ticketID, adminID, "status", desc)
	return nil
}

// ---- 用户工单：评分 / 催单 ----

// ScoreTicketPremium 用户评分（满意度 / 服务态度 / 处理时效，0.5 ~ 5 分）。
func (s *Store) ScoreTicketPremium(ctx context.Context, ticketPublicID string, userID int64, satisfaction, attitude, processing float64) (float64, error) {
	if satisfaction < 0.5 || satisfaction > 5 || attitude < 0.5 || attitude > 5 || processing < 0.5 || processing > 5 {
		return 0, ErrTicketScoreRange
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	var ticketID, ownerID int64
	var finished, scored bool
	err = tx.QueryRow(ctx, `SELECT id,user_id,finished,is_score FROM tickets WHERE public_id=$1 FOR UPDATE`, ticketPublicID).Scan(&ticketID, &ownerID, &finished, &scored)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, err
	}
	if ownerID != userID {
		return 0, ErrNotFound
	}
	if !finished {
		return 0, ErrTicketNotFinished
	}
	if scored {
		return 0, ErrTicketAlreadyScored
	}
	if _, err := tx.Exec(ctx, `UPDATE tickets SET is_score=TRUE,satisfaction=$2,attitude=$3,processing_time=$4,score_time=now(),updated_at=now() WHERE id=$1`,
		ticketID, satisfaction, attitude, processing); err != nil {
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	_ = s.addTicketLog(ctx, ticketID, 0, "score", "用户提交评分")
	return (satisfaction + attitude + processing) / 3, nil
}

// UrgeTicket 用户催单（15 分钟内只允许一次）。
func (s *Store) UrgeTicket(ctx context.Context, ticketPublicID string, userID int64) error {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var ticketID, ownerID int64
	var lastUrge *time.Time
	err = tx.QueryRow(ctx, `SELECT id,user_id,last_urge_time FROM tickets WHERE public_id=$1 FOR UPDATE`, ticketPublicID).Scan(&ticketID, &ownerID, &lastUrge)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if ownerID != userID {
		return ErrNotFound
	}
	if lastUrge != nil && time.Since(*lastUrge) < 15*time.Minute {
		return ErrTicketRateLimited
	}
	if _, err := tx.Exec(ctx, `UPDATE tickets SET last_urge_time=now(),urge_count=urge_count+1 WHERE id=$1`, ticketID); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	_ = s.addTicketLog(ctx, ticketID, 0, "urge", "用户催单")
	return nil
}

// ---- 转内部工单 ----

// TurnTicketInternal 把用户工单转成内部工单（TicketInternalPremium 协作）。
func (s *Store) TurnTicketInternal(ctx context.Context, sourcePublicID string, creatorID int64, title string, typeID int64, priority string, clientID int64, hostIDs []int64) (int64, string, error) {
	var deptID int64
	err := s.DB.QueryRow(ctx, `SELECT department_id FROM ticket_internal_types WHERE id=$1`, typeID).Scan(&deptID)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, "", errors.New("内部工单类型不存在")
	}
	if err != nil {
		return 0, "", err
	}
	if clientID <= 0 {
		_ = s.DB.QueryRow(ctx, `SELECT user_id FROM tickets WHERE public_id=$1`, sourcePublicID).Scan(&clientID)
	}
	id, number, err := s.CreateTicketInternal(ctx, TicketInternalCreateInput{
		Title:        title,
		DepartmentID: deptID,
		TypeID:       typeID,
		ClientID:     clientID,
		HostIDs:      hostIDs,
		SourceTicket: sourcePublicID,
		Priority:     priority,
		CreatorID:    creatorID,
	})
	if err != nil {
		return 0, "", err
	}
	if ticketID, err := s.ticketIDByPublic(ctx, sourcePublicID); err == nil {
		_ = s.addTicketLog(ctx, ticketID, creatorID, "turn_internal", "转为内部工单 "+number)
	}
	return id, number, nil
}

// ---- 工单统计 ----

// TicketStatsFilter 是统计范围（所有 / 部门 / 个人 + 时间范围）。
type TicketStatsFilter struct {
	Scope        string
	DepartmentID int64
	AdminID      int64
	Start        *time.Time
	End          *time.Time
}

// TicketStatsSummary 是统计卡数据。
type TicketStatsSummary struct {
	Total                 int64   `json:"total"`
	PendingTotal          int64   `json:"pending_total"`
	ProcessedTotal        int64   `json:"processed_total"`
	AvgScore              float64 `json:"avg_score"`
	Satisfaction          float64 `json:"satisfaction"`
	Attitude              float64 `json:"attitude"`
	ScoreProcessing       float64 `json:"score_processing"`
	AverageProcessingTime float64 `json:"average_processing_time"`
	ScoreTotal            int64   `json:"score_total"`
	NotScoreTotal         int64   `json:"not_score_total"`
	ScoreRate             float64 `json:"score_rate"`
	OvertimeRatio         float64 `json:"overtime_ratio"`
}

// TicketRankRow 是部门 / 个人排名行。
type TicketRankRow struct {
	Name         string  `json:"name"`
	Score        float64 `json:"score"`
	Satisfaction float64 `json:"satisfaction"`
	Attitude     float64 `json:"attitude"`
	Processing   float64 `json:"processing"`
}

func (f TicketStatsFilter) where(alias string) (string, []any) {
	where := []string{"TRUE"}
	args := []any{}
	add := func(v any) string {
		args = append(args, v)
		return fmt.Sprintf("$%d", len(args))
	}
	if f.Scope == "department" && f.DepartmentID > 0 {
		where = append(where, alias+".department_id = "+add(f.DepartmentID))
	}
	if f.Scope == "admin" && f.AdminID > 0 {
		where = append(where, alias+".admin_id = "+add(f.AdminID))
	}
	if f.Start != nil {
		where = append(where, alias+".created_at >= "+add(*f.Start))
	}
	if f.End != nil {
		where = append(where, alias+".created_at <= "+add(*f.End))
	}
	return strings.Join(where, " AND "), args
}

// TicketPremiumStatistics 统计卡（单量 / 评分 / 时长 / 超时占比）。
func (s *Store) TicketPremiumStatistics(ctx context.Context, f TicketStatsFilter) (TicketStatsSummary, error) {
	var out TicketStatsSummary
	var overtimeCount, dueCount int64
	clause, args := f.where("t")
	err := s.DB.QueryRow(ctx, `SELECT count(*),
coalesce(sum(CASE WHEN t.finished THEN 0 ELSE 1 END),0),
coalesce(sum(CASE WHEN t.finished THEN 1 ELSE 0 END),0),
coalesce(sum(CASE WHEN t.is_score THEN 1 ELSE 0 END),0),
coalesce(sum(CASE WHEN t.is_score THEN 0 ELSE 1 END),0),
coalesce(avg(CASE WHEN t.is_score THEN (t.satisfaction+t.attitude+t.processing_time)/3 END),0)::float8,
coalesce(avg(CASE WHEN t.is_score THEN t.satisfaction END),0)::float8,
coalesce(avg(CASE WHEN t.is_score THEN t.attitude END),0)::float8,
coalesce(avg(CASE WHEN t.is_score THEN t.processing_time END),0)::float8,
coalesce(avg(CASE WHEN t.finish_time IS NOT NULL THEN extract(epoch FROM (t.finish_time-t.created_at)) END),0)::float8,
coalesce(sum(CASE WHEN tt.processing_limit > 0 AND ((t.finish_time IS NOT NULL AND t.finish_time > t.created_at + (tt.processing_limit * interval '1 hour')) OR (t.finish_time IS NULL AND now() > t.created_at + (tt.processing_limit * interval '1 hour'))) THEN 1 ELSE 0 END),0),
coalesce(sum(CASE WHEN tt.processing_limit > 0 THEN 1 ELSE 0 END),0)
FROM tickets t LEFT JOIN ticket_types tt ON tt.id=t.ticket_type_id
WHERE `+clause, args...).Scan(&out.Total, &out.PendingTotal, &out.ProcessedTotal, &out.ScoreTotal, &out.NotScoreTotal,
		&out.AvgScore, &out.Satisfaction, &out.Attitude, &out.ScoreProcessing, &out.AverageProcessingTime,
		&overtimeCount, &dueCount)
	if err != nil {
		return out, err
	}
	if out.ScoreTotal+out.NotScoreTotal > 0 {
		out.ScoreRate = float64(out.ScoreTotal) / float64(out.ScoreTotal+out.NotScoreTotal)
	}
	if dueCount > 0 {
		out.OvertimeRatio = float64(overtimeCount) / float64(dueCount)
	}
	return out, nil
}

// TicketPremiumScoreRank 平均分排名（部门或个人）。
func (s *Store) TicketPremiumScoreRank(ctx context.Context, byDepartment bool, f TicketStatsFilter) ([]TicketRankRow, error) {
	clause, args := f.where("t")
	group := "t.admin_id"
	nameExpr := `coalesce((SELECT coalesce(nullif(pf.nickname,''),u2.email,'') FROM users u2 LEFT JOIN user_profiles pf ON pf.user_id=u2.id WHERE u2.id=t.admin_id),'--')`
	if byDepartment {
		group = "t.department_id"
		nameExpr = "coalesce((SELECT d.name FROM ticket_departments d WHERE d.id=t.department_id),'--')"
	}
	q := `SELECT ` + nameExpr + ` AS name,
coalesce(avg((t.satisfaction+t.attitude+t.processing_time)/3),0)::float8,
coalesce(avg(t.satisfaction),0)::float8,
coalesce(avg(t.attitude),0)::float8,
coalesce(avg(t.processing_time),0)::float8
FROM tickets t WHERE ` + clause + ` AND t.is_score=TRUE AND ` + group + ` IS NOT NULL
GROUP BY ` + group + ` ORDER BY 2 DESC`
	rows, err := s.DB.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TicketRankRow{}
	for rows.Next() {
		var v TicketRankRow
		if err := rows.Scan(&v.Name, &v.Score, &v.Satisfaction, &v.Attitude, &v.Processing); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// TicketPremiumTimeRank 平均处理时长排名（部门或个人，单位秒，快者在前）。
func (s *Store) TicketPremiumTimeRank(ctx context.Context, byDepartment bool, f TicketStatsFilter) ([]TicketRankRow, error) {
	clause, args := f.where("t")
	group := "t.admin_id"
	nameExpr := `coalesce((SELECT coalesce(nullif(pf.nickname,''),u2.email,'') FROM users u2 LEFT JOIN user_profiles pf ON pf.user_id=u2.id WHERE u2.id=t.admin_id),'--')`
	if byDepartment {
		group = "t.department_id"
		nameExpr = "coalesce((SELECT d.name FROM ticket_departments d WHERE d.id=t.department_id),'--')"
	}
	q := `SELECT ` + nameExpr + ` AS name,
coalesce(avg(extract(epoch FROM (t.finish_time-t.created_at))),0)::float8
FROM tickets t WHERE ` + clause + ` AND t.finish_time IS NOT NULL AND ` + group + ` IS NOT NULL
GROUP BY ` + group + ` ORDER BY 2 ASC`
	rows, err := s.DB.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TicketRankRow{}
	for rows.Next() {
		var v TicketRankRow
		if err := rows.Scan(&v.Name, &v.Score); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
