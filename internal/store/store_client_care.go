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

// 客户关怀（对齐魔方 CBAP ClientCare 插件）。
//
// 后台按条件圈人（push_object：指定用户 / 指定产品 / 指定接口 + 注册时长、
// 产品数量、上次登录、产品状态、购买/删除时间等）创建推送任务，支持一次性 /
// 每天 / 每周 / 每月；调度器到点把站内信写进用户收件箱（client_care_mails），
// 邮件类任务同时交给队列按指定通道发信。本站短信通道只支持验证码模板，
// 自定义内容的短信推送暂不投递（字段保留）。
//
// push_object 结构（与原插件前端一致）：
//   {"condition1":"client|host|server","condition2":"...","condition3":[...]|">=","condition4":30,"condition5":"day|date"}

const clientCareJobCols = `SELECT cj.id,cj.public_id::text,cj.title,cj.type,cj.content,cj.subject,cj.email_name,cj.sms_name,cj.sms_template_id,
cj.push_start_time,cj.push_end_time,cj.send_cycle,cj.week_day,cj.month_day,cj.hour,cj.minute,cj.repeat_send,cj.push_object,
cj.status,cj.next_run_at,cj.last_run_at,cj.created_at,cj.updated_at
FROM client_care_jobs cj`

// ClientCareJob 是一条推送任务。
type ClientCareJob struct {
	ID            int64           `json:"id"`
	PublicID      string          `json:"public_id"`
	Title         string          `json:"title"`
	Type          int             `json:"type"`
	Content       string          `json:"content"`
	Subject       string          `json:"subject"`
	EmailName     string          `json:"email_name"`
	SmsName       string          `json:"sms_name"`
	SmsTemplateID int64           `json:"sms_template_id"`
	PushStartTime time.Time       `json:"push_start_time"`
	PushEndTime   time.Time       `json:"push_end_time"`
	SendCycle     string          `json:"send_cycle"`
	WeekDay       int             `json:"week_day"`
	MonthDay      int             `json:"month_day"`
	Hour          int             `json:"hour"`
	Minute        int             `json:"minute"`
	RepeatSend    bool            `json:"repeat_send"`
	PushObject    json.RawMessage `json:"push_object"`
	Status        string          `json:"status"`
	NextRunAt     *time.Time      `json:"next_run_at"`
	LastRunAt     *time.Time      `json:"last_run_at"`
	CreatedAt     time.Time       `json:"created_at"`
	UpdatedAt     time.Time       `json:"updated_at"`
}

// ClientCareRecipient 是命中筛选条件的用户。
type ClientCareRecipient struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
	Email    string `json:"email"`
	Phone    string `json:"phone"`
	SendNum  int    `json:"send_num"`
}

// ClientCareDelivery 是一次需要发邮件的投递。
type ClientCareDelivery struct {
	MailPublicID string
	UserID       int64
	Email        string
	Subject      string
	Content      string
	MailProvider string
}

// ClientCareMail 是用户收件箱里的站内信。
type ClientCareMail struct {
	ID        int64      `json:"id"`
	PublicID  string     `json:"public_id"`
	Title     string     `json:"title"`
	Content   string     `json:"content"`
	ReadAt    *time.Time `json:"read_at"`
	CreatedAt time.Time  `json:"created_at"`
	PrevID    string     `json:"prev_id,omitempty"`
	NextID    string     `json:"next_id,omitempty"`
}

// ClientCareJobInput 是创建推送任务的入参。
type ClientCareJobInput struct {
	Title         string
	Type          int
	Content       string
	Subject       string
	EmailName     string
	SmsName       string
	SmsTemplateID int64
	PushStartTime time.Time
	PushEndTime   time.Time
	SendCycle     string
	WeekDay       int
	MonthDay      int
	Hour          int
	Minute        int
	RepeatSend    bool
	PushObject    json.RawMessage
}

// ClientCareOption 是表单下拉项。
type ClientCareOption struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// ClientCareOptions 是推送表单需要的全部选项。
type ClientCareOptions struct {
	Products      []ClientCareOption  `json:"products"`
	Providers     []ClientCareOption  `json:"providers"`
	MailProviders []ClientCareOption  `json:"mail_providers"`
	MailTemplates []map[string]string `json:"mail_templates"`
}

func scanClientCareJob(row pgx.Row) (ClientCareJob, error) {
	var v ClientCareJob
	err := row.Scan(&v.ID, &v.PublicID, &v.Title, &v.Type, &v.Content, &v.Subject, &v.EmailName, &v.SmsName, &v.SmsTemplateID,
		&v.PushStartTime, &v.PushEndTime, &v.SendCycle, &v.WeekDay, &v.MonthDay, &v.Hour, &v.Minute, &v.RepeatSend, &v.PushObject,
		&v.Status, &v.NextRunAt, &v.LastRunAt, &v.CreatedAt, &v.UpdatedAt)
	return v, err
}

// ---- 条件解析 ----

type clientCareCondition struct {
	Condition1 string          `json:"condition1"`
	Condition2 string          `json:"condition2"`
	Condition3 json.RawMessage `json:"condition3"`
	Condition4 json.RawMessage `json:"condition4"`
	Condition5 string          `json:"condition5"`
}

func rawToInt64Slice(raw json.RawMessage) []int64 {
	out := []int64{}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &out)
	}
	return out
}

func rawToStringSlice(raw json.RawMessage) []string {
	out := []string{}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &out)
	}
	return out
}

func rawToString(raw json.RawMessage) string {
	var s string
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &s)
	}
	return s
}

func rawToInt(raw json.RawMessage) int {
	var n int
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &n)
		if n == 0 {
			var f float64
			if err := json.Unmarshal(raw, &f); err == nil {
				n = int(f)
			}
		}
	}
	return n
}

// mapClientCareHostStatuses 把原插件的产品状态映射到本站服务状态。
func mapClientCareHostStatuses(in []string) []string {
	m := map[string]string{
		"Unpaid":    "pending",
		"Pending":   "provisioning",
		"Active":    "active",
		"Suspended": "suspended",
		"Deleted":   "terminated",
		"Failed":    "failed",
		"Cancelled": "",
	}
	out := []string{}
	seen := map[string]bool{}
	for _, v := range in {
		if sv, ok := m[v]; ok && sv != "" && !seen[sv] {
			seen[sv] = true
			out = append(out, sv)
		}
	}
	return out
}

// clientCareConditionWhere 把 push_object 翻译成 users 表的 WHERE 片段。
func clientCareConditionWhere(obj clientCareCondition) (string, []any) {
	where := []string{"u.deleted_at IS NULL"}
	args := []any{}
	placeholder := func(v any) string {
		args = append(args, v)
		return fmt.Sprintf("$%d", len(args))
	}
	serviceExists := func(cond string) string {
		return "EXISTS (SELECT 1 FROM services sv WHERE sv.user_id=u.id AND " + cond + ")"
	}
	switch obj.Condition1 {
	case "client":
		switch obj.Condition2 {
		case "client":
			ids := rawToInt64Slice(obj.Condition3)
			if len(ids) == 0 {
				return "FALSE", nil
			}
			where = append(where, "u.id = ANY("+placeholder(ids)+"::bigint[])")
		case "register_time":
			days := rawToInt(obj.Condition4)
			if rawToString(obj.Condition3) == "<" {
				where = append(where, "u.created_at > now() - make_interval(days => "+placeholder(days)+")")
			} else {
				where = append(where, "u.created_at <= now() - make_interval(days => "+placeholder(days)+")")
			}
		case "last_login_time":
			days := rawToInt(obj.Condition4)
			expr := "COALESCE((SELECT max(l.created_at) FROM user_login_logs l WHERE l.user_id=u.id AND l.success), 'epoch'::timestamptz)"
			if rawToString(obj.Condition3) == "<" {
				where = append(where, expr+" > now() - make_interval(days => "+placeholder(days)+")")
			} else {
				where = append(where, expr+" <= now() - make_interval(days => "+placeholder(days)+")")
			}
		case "active_host_num", "host_num":
			num := rawToInt(obj.Condition4)
			cond := "sv.status <> 'terminated'"
			if obj.Condition2 == "active_host_num" {
				cond = "sv.status = 'active'"
			}
			expr := "(SELECT count(*) FROM services sv WHERE sv.user_id=u.id AND " + cond + ")"
			if rawToString(obj.Condition3) == "<" {
				where = append(where, expr+" < "+placeholder(num))
			} else {
				where = append(where, expr+" >= "+placeholder(num))
			}
		case "owner_special_product":
			ids := rawToStringSlice(obj.Condition3)
			if len(ids) == 0 {
				return "FALSE", nil
			}
			where = append(where, serviceExists("sv.status <> 'terminated' AND sv.product_id IN (SELECT p.id FROM products p WHERE p.public_id::text = ANY("+placeholder(ids)+"::text[]))"))
		default:
			return "FALSE", nil
		}
	case "host":
		switch obj.Condition2 {
		case "status":
			statuses := mapClientCareHostStatuses(rawToStringSlice(obj.Condition3))
			if len(statuses) == 0 {
				return "FALSE", nil
			}
			where = append(where, serviceExists("sv.status = ANY("+placeholder(statuses)+"::text[])"))
		case "purchase_time", "termination_time":
			col := "sv.created_at"
			if obj.Condition2 == "termination_time" {
				col = "sv.terminated_at"
			}
			cmp := rawToString(obj.Condition3)
			if obj.Condition5 == "date" {
				ts := rawToString(obj.Condition4)
				if ts == "" {
					return "FALSE", nil
				}
				if cmp == "<" {
					where = append(where, serviceExists(col+" < "+placeholder(ts)+"::date"))
				} else {
					where = append(where, serviceExists(col+" >= "+placeholder(ts)+"::date"))
				}
			} else {
				days := rawToInt(obj.Condition4)
				if cmp == "<" {
					where = append(where, serviceExists(col+" > now() - make_interval(days => "+placeholder(days)+")"))
				} else {
					where = append(where, serviceExists(col+" <= now() - make_interval(days => "+placeholder(days)+")"))
				}
			}
		default:
			return "FALSE", nil
		}
	case "server":
		if obj.Condition2 != "product" {
			return "FALSE", nil
		}
		ids := rawToStringSlice(obj.Condition3)
		if len(ids) == 0 {
			return "FALSE", nil
		}
		where = append(where, serviceExists("sv.provider_id IN (SELECT pv.id FROM providers pv WHERE pv.public_id::text = ANY("+placeholder(ids)+"::text[]))"))
	default:
		return "FALSE", nil
	}
	return strings.Join(where, " AND "), args
}

// ClientCareRecipients 按 push_object 圈人（预览与执行共用）。
func (s *Store) ClientCareRecipients(ctx context.Context, pushObject json.RawMessage, limit int) ([]ClientCareRecipient, int64, error) {
	var obj clientCareCondition
	if len(pushObject) > 0 {
		if err := json.Unmarshal(pushObject, &obj); err != nil {
			return nil, 0, err
		}
	}
	if limit <= 0 || limit > 5000 {
		limit = 500
	}
	where, args := clientCareConditionWhere(obj)
	var total int64
	if err := s.DB.QueryRow(ctx, `SELECT count(*) FROM users u WHERE `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	args = append(args, limit)
	rows, err := s.DB.Query(ctx, `SELECT u.id,u.email,COALESCE(u.phone,'') FROM users u WHERE `+where+fmt.Sprintf(` ORDER BY u.id LIMIT $%d`, len(args)), args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []ClientCareRecipient{}
	for rows.Next() {
		var v ClientCareRecipient
		if err := rows.Scan(&v.ID, &v.Email, &v.Phone); err != nil {
			return nil, 0, err
		}
		v.Username = v.Email
		v.SendNum = 1
		out = append(out, v)
	}
	return out, total, rows.Err()
}

// ClientCareUserSearch 供「指定用户」条件搜索用户。
func (s *Store) ClientCareUserSearch(ctx context.Context, keyword string, limit int) ([]ClientCareRecipient, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	keyword = strings.TrimSpace(keyword)
	rows, err := s.DB.Query(ctx, `SELECT u.id,u.email,COALESCE(u.phone,'') FROM users u
WHERE u.deleted_at IS NULL AND ($1='' OR u.email ILIKE '%'||$1||'%' OR COALESCE(u.phone,'') ILIKE '%'||$1||'%')
ORDER BY u.id DESC LIMIT $2`, keyword, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ClientCareRecipient{}
	for rows.Next() {
		var v ClientCareRecipient
		if err := rows.Scan(&v.ID, &v.Email, &v.Phone); err != nil {
			return nil, err
		}
		v.Username = v.Email
		v.SendNum = 1
		out = append(out, v)
	}
	return out, rows.Err()
}

// ClientCareOptions 返回推送表单需要的数据源。
func (s *Store) ClientCareOptions(ctx context.Context) (ClientCareOptions, error) {
	out := ClientCareOptions{Products: []ClientCareOption{}, Providers: []ClientCareOption{}, MailProviders: []ClientCareOption{}, MailTemplates: []map[string]string{}}
	rows, err := s.DB.Query(ctx, `SELECT public_id::text,name FROM products WHERE deleted_at IS NULL ORDER BY name`)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var v ClientCareOption
		if err := rows.Scan(&v.ID, &v.Name); err != nil {
			rows.Close()
			return out, err
		}
		out.Products = append(out.Products, v)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return out, err
	}
	prows, err := s.DB.Query(ctx, `SELECT public_id::text,name FROM providers WHERE active ORDER BY name`)
	if err != nil {
		return out, err
	}
	for prows.Next() {
		var v ClientCareOption
		if err := prows.Scan(&v.ID, &v.Name); err != nil {
			prows.Close()
			return out, err
		}
		out.Providers = append(out.Providers, v)
	}
	prows.Close()
	if err := prows.Err(); err != nil {
		return out, err
	}
	if mailProviders, err := s.ListMailProviders(ctx); err == nil {
		for _, p := range mailProviders {
			out.MailProviders = append(out.MailProviders, ClientCareOption{ID: p.PublicID, Name: p.Name})
		}
	}
	if templates, err := s.ListMailTemplates(ctx); err == nil {
		for _, t := range templates {
			out.MailTemplates = append(out.MailTemplates, map[string]string{"name": t.Name, "subject": t.Subject})
		}
	}
	return out, nil
}

// ---- 任务 CRUD ----

// ListClientCareJobs 分页列出推送任务。
func (s *Store) ListClientCareJobs(ctx context.Context, keyword, status string, limit, offset int) ([]ClientCareJob, int64, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	if offset < 0 {
		offset = 0
	}
	keyword = strings.TrimSpace(keyword)
	where := ` WHERE ($1='' OR cj.title ILIKE '%'||$1||'%') AND ($2='' OR cj.status=$2)`
	rows, err := s.DB.Query(ctx, clientCareJobCols+where+` ORDER BY cj.id DESC LIMIT $3 OFFSET $4`, keyword, status, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []ClientCareJob{}
	for rows.Next() {
		v, err := scanClientCareJob(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	var total int64
	if err := s.DB.QueryRow(ctx, `SELECT count(*) FROM client_care_jobs cj`+where, keyword, status).Scan(&total); err != nil {
		return nil, 0, err
	}
	return out, total, nil
}

// GetClientCareJob 读取单个任务。
func (s *Store) GetClientCareJob(ctx context.Context, publicID string) (ClientCareJob, error) {
	v, err := scanClientCareJob(s.DB.QueryRow(ctx, clientCareJobCols+` WHERE cj.public_id=$1`, publicID))
	if errors.Is(err, pgx.ErrNoRows) {
		return ClientCareJob{}, ErrNotFound
	}
	return v, err
}

// nextClientCareRun 计算 from 之后的下一次推送时间；返回 false 表示超出结束时间。
func nextClientCareRun(jobStart, jobEnd time.Time, cycle string, weekDay, monthDay, hour, minute int, from time.Time) (time.Time, bool) {
	if hour < 0 {
		hour = 0
	}
	if hour > 23 {
		hour = 23
	}
	if minute < 0 {
		minute = 0
	}
	if minute > 59 {
		minute = 59
	}
	if cycle == "onetime" {
		t := jobStart
		if t.Before(from) {
			t = from
		}
		if t.After(jobEnd) {
			return time.Time{}, false
		}
		return t, true
	}
	// 时间点（hour/minute / 星期 / 日期）按服务器本地时区解释。
	loc := time.Local
	var candidate time.Time
	switch cycle {
	case "day":
		candidate = time.Date(from.Year(), from.Month(), from.Day(), hour, minute, 0, 0, loc)
		if !candidate.After(from) {
			candidate = candidate.AddDate(0, 0, 1)
		}
	case "week":
		wd := weekDay % 7
		if wd < 0 {
			wd = 1
		}
		for i := 0; i < 9 && candidate.IsZero(); i++ {
			t := time.Date(from.Year(), from.Month(), from.Day(), hour, minute, 0, 0, loc).AddDate(0, 0, i)
			if int(t.Weekday()) == wd && t.After(from) {
				candidate = t
			}
		}
	case "month":
		day := monthDay
		if day < 1 {
			day = 1
		}
		for i := 0; i < 14 && candidate.IsZero(); i++ {
			y, m := from.Year(), from.Month()
			first := time.Date(y, m, 1, 0, 0, 0, 0, loc).AddDate(0, i, 0)
			last := first.AddDate(0, 1, -1).Day()
			d := day
			if d > last {
				d = last
			}
			t := time.Date(first.Year(), first.Month(), d, hour, minute, 0, 0, loc)
			if t.After(from) {
				candidate = t
			}
		}
	default:
		return time.Time{}, false
	}
	if candidate.IsZero() {
		return time.Time{}, false
	}
	for i := 0; i < 400 && candidate.Before(jobStart); i++ {
		switch cycle {
		case "day":
			candidate = candidate.AddDate(0, 0, 1)
		case "week":
			candidate = candidate.AddDate(0, 0, 7)
		case "month":
			candidate = candidate.AddDate(0, 1, 0)
		}
	}
	if candidate.After(jobEnd) {
		return time.Time{}, false
	}
	return candidate, true
}

// CreateClientCareJob 新建推送任务并算出首次执行时间。
func (s *Store) CreateClientCareJob(ctx context.Context, in ClientCareJobInput) (ClientCareJob, error) {
	if len(in.PushObject) == 0 {
		in.PushObject = json.RawMessage(`{}`)
	}
	now := time.Now().UTC()
	next, ok := nextClientCareRun(in.PushStartTime, in.PushEndTime, in.SendCycle, in.WeekDay, in.MonthDay, in.Hour, in.Minute, now)
	status := "wait"
	var nextAt *time.Time
	if ok {
		nextAt = &next
	} else {
		status = "expired"
	}
	var publicID string
	err := s.DB.QueryRow(ctx, `INSERT INTO client_care_jobs(title,type,content,subject,email_name,sms_name,sms_template_id,
push_start_time,push_end_time,send_cycle,week_day,month_day,hour,minute,repeat_send,push_object,status,next_run_at)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18) RETURNING public_id::text`,
		strings.TrimSpace(in.Title), in.Type, in.Content, strings.TrimSpace(in.Subject), strings.TrimSpace(in.EmailName), strings.TrimSpace(in.SmsName), in.SmsTemplateID,
		in.PushStartTime, in.PushEndTime, in.SendCycle, in.WeekDay, in.MonthDay, in.Hour, in.Minute, in.RepeatSend, []byte(in.PushObject), status, nextAt).Scan(&publicID)
	if err != nil {
		return ClientCareJob{}, err
	}
	return s.GetClientCareJob(ctx, publicID)
}

// SetClientCareJobStatus 启用 / 停用任务；启用时重算下一次执行时间。
func (s *Store) SetClientCareJobStatus(ctx context.Context, publicID string, enable bool) (ClientCareJob, error) {
	job, err := s.GetClientCareJob(ctx, publicID)
	if err != nil {
		return ClientCareJob{}, err
	}
	if !enable {
		if _, err := s.DB.Exec(ctx, `UPDATE client_care_jobs SET status='suspended',next_run_at=NULL,updated_at=now() WHERE id=$1`, job.ID); err != nil {
			return ClientCareJob{}, err
		}
		return s.GetClientCareJob(ctx, publicID)
	}
	now := time.Now().UTC()
	next, ok := nextClientCareRun(job.PushStartTime, job.PushEndTime, job.SendCycle, job.WeekDay, job.MonthDay, job.Hour, job.Minute, now)
	status := "wait"
	var nextAt *time.Time
	if ok {
		nextAt = &next
	} else {
		status = "expired"
	}
	if _, err := s.DB.Exec(ctx, `UPDATE client_care_jobs SET status=$2,next_run_at=$3,updated_at=now() WHERE id=$1`, job.ID, status, nextAt); err != nil {
		return ClientCareJob{}, err
	}
	return s.GetClientCareJob(ctx, publicID)
}

// DeleteClientCareJob 删除任务（收件箱保留历史）。
func (s *Store) DeleteClientCareJob(ctx context.Context, publicID string) error {
	tag, err := s.DB.Exec(ctx, `DELETE FROM client_care_jobs WHERE public_id=$1`, publicID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ---- 执行与投递 ----

// excerptClientCare 去掉 HTML 标签，供通知摘要使用。
func excerptClientCare(html string, n int) string {
	var b strings.Builder
	inTag := false
	for _, r := range html {
		switch {
		case r == '<':
			inTag = true
		case r == '>':
			inTag = false
		case !inTag:
			b.WriteRune(r)
		}
	}
	s := strings.Join(strings.Fields(b.String()), " ")
	r := []rune(s)
	if len(r) > n {
		s = string(r[:n]) + "…"
	}
	return s
}

// RunDueClientCareJobs 把到点的任务投递出去：站内信进收件箱 + 生成需要发邮件的清单。
func (s *Store) RunDueClientCareJobs(ctx context.Context, limit int) ([]ClientCareDelivery, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	rows, err := s.DB.Query(ctx, clientCareJobCols+`
WHERE cj.status IN ('wait','exec') AND cj.next_run_at IS NOT NULL AND cj.next_run_at <= now()
ORDER BY cj.next_run_at LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	jobs := []ClientCareJob{}
	for rows.Next() {
		v, err := scanClientCareJob(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		jobs = append(jobs, v)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	deliveries := []ClientCareDelivery{}
	for _, job := range jobs {
		if job.PushEndTime.Before(now) {
			if _, err := s.DB.Exec(ctx, `UPDATE client_care_jobs SET status='expired',next_run_at=NULL,updated_at=now() WHERE id=$1`, job.ID); err != nil {
				return deliveries, err
			}
			continue
		}
		recipients, _, err := s.ClientCareRecipients(ctx, job.PushObject, 2000)
		if err != nil {
			return deliveries, err
		}
		for _, r := range recipients {
			if !job.RepeatSend {
				var exists bool
				if err := s.DB.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM client_care_mails WHERE job_id=$1 AND user_id=$2)`, job.ID, r.ID).Scan(&exists); err != nil {
					return deliveries, err
				}
				if exists {
					continue
				}
			}
			var mailPublicID string
			if err := s.DB.QueryRow(ctx, `INSERT INTO client_care_mails(user_id,job_id,title,content) VALUES($1,$2,$3,$4) RETURNING public_id::text`,
				r.ID, job.ID, job.Title, job.Content).Scan(&mailPublicID); err != nil {
				return deliveries, err
			}
			_ = s.InsertNotification(ctx, r.ID, "client_care", job.Title, excerptClientCare(job.Content, 120), "/messages/"+mailPublicID)
			if job.Type == 2 && strings.TrimSpace(r.Email) != "" && strings.TrimSpace(job.Subject) != "" {
				deliveries = append(deliveries, ClientCareDelivery{
					MailPublicID: mailPublicID,
					UserID:       r.ID,
					Email:        r.Email,
					Subject:      job.Subject,
					Content:      job.Content,
					MailProvider: job.EmailName,
				})
			}
		}
		status := "exec"
		var nextAt *time.Time
		if job.SendCycle == "onetime" {
			status = "finished"
		} else if t, ok := nextClientCareRun(job.PushStartTime, job.PushEndTime, job.SendCycle, job.WeekDay, job.MonthDay, job.Hour, job.Minute, now); ok {
			nextAt = &t
		} else {
			status = "expired"
		}
		if _, err := s.DB.Exec(ctx, `UPDATE client_care_jobs SET status=$2,next_run_at=$3,last_run_at=now(),updated_at=now() WHERE id=$1`, job.ID, status, nextAt); err != nil {
			return deliveries, err
		}
	}
	return deliveries, nil
}

// ---- 用户收件箱 ----

// ListUserClientCareMails 用户的消息列表。
func (s *Store) ListUserClientCareMails(ctx context.Context, userID int64) ([]ClientCareMail, error) {
	rows, err := s.DB.Query(ctx, `SELECT id,public_id::text,title,read_at,created_at FROM client_care_mails WHERE user_id=$1 ORDER BY id DESC LIMIT 200`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ClientCareMail{}
	for rows.Next() {
		var v ClientCareMail
		if err := rows.Scan(&v.ID, &v.PublicID, &v.Title, &v.ReadAt, &v.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// GetUserClientCareMail 读取一封消息（含上一篇 / 下一篇）。
func (s *Store) GetUserClientCareMail(ctx context.Context, userID int64, publicID string) (ClientCareMail, error) {
	var v ClientCareMail
	err := s.DB.QueryRow(ctx, `SELECT id,public_id::text,title,content,read_at,created_at FROM client_care_mails WHERE user_id=$1 AND public_id=$2`, userID, publicID).
		Scan(&v.ID, &v.PublicID, &v.Title, &v.Content, &v.ReadAt, &v.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return ClientCareMail{}, ErrNotFound
	}
	if err != nil {
		return ClientCareMail{}, err
	}
	_ = s.DB.QueryRow(ctx, `SELECT public_id::text FROM client_care_mails WHERE user_id=$1 AND id < $2 ORDER BY id DESC LIMIT 1`, userID, v.ID).Scan(&v.PrevID)
	_ = s.DB.QueryRow(ctx, `SELECT public_id::text FROM client_care_mails WHERE user_id=$1 AND id > $2 ORDER BY id ASC LIMIT 1`, userID, v.ID).Scan(&v.NextID)
	return v, nil
}

// MarkClientCareMailRead 标记已读。
func (s *Store) MarkClientCareMailRead(ctx context.Context, userID int64, publicID string) error {
	tag, err := s.DB.Exec(ctx, `UPDATE client_care_mails SET read_at=now() WHERE user_id=$1 AND public_id=$2 AND read_at IS NULL`, userID, publicID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		var exists bool
		if err := s.DB.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM client_care_mails WHERE user_id=$1 AND public_id=$2)`, userID, publicID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return ErrNotFound
		}
	}
	return nil
}
