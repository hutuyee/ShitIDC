package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/hutuyee/ShitIDC/internal/model"
)

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

func fmtErrDuplicate(msg string) error { return fmt.Errorf("%s", msg) }

func jsonUnmarshal(data []byte, v any) error { return json.Unmarshal(data, v) }

// Store queries for the MVP gap fill: API request logs (§5.6), device
// management (§8/9), product groups (第五阶段), announcements, invoice and
// service detail, and TOTP 2FA (§9).

// ---- API request logs ----

// APILogEntry is one request observation written asynchronously by the
// middleware. Query strings are never recorded.
type APILogEntry struct {
	Method     string
	Path       string
	Status     int
	UserID     int64
	APIToken   bool
	ErrorCode  string
	RequestID  string
	IP         string
	UserAgent  string
	DurationMs int
}

// InsertAPILogs writes a batch in one transaction; log writes must never
// break or slow the request path, so failures are swallowed by the caller.
func (s *Store) InsertAPILogs(ctx context.Context, entries []APILogEntry) error {
	if len(entries) == 0 {
		return nil
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	for _, e := range entries {
		var uid any
		if e.UserID > 0 {
			uid = e.UserID
		}
		if _, err := tx.Exec(ctx, `INSERT INTO api_logs(method,path,status,user_id,api_token,error_code,request_id,ip,user_agent,duration_ms)
VALUES($1,$2,$3,$4,$5,NULLIF($6,''),NULLIF($7,''),NULLIF($8,'')::inet,$9,$10)`,
			e.Method, e.Path, e.Status, uid, e.APIToken, e.ErrorCode, e.RequestID, e.IP, e.UserAgent, e.DurationMs); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// ListAPILogs returns recent request logs for the admin console.
func (s *Store) ListAPILogs(ctx context.Context, method, path string, statusMin int, userID int64, limit int) ([]map[string]any, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.DB.Query(ctx, `SELECT l.id,l.method,l.path,l.status,coalesce(l.user_id,0),l.api_token,l.error_code,l.request_id,coalesce(l.ip::text,''),l.user_agent,l.duration_ms,l.created_at
FROM api_logs l
WHERE ($1='' OR l.method=$1) AND ($2='' OR l.path LIKE '%'||$2||'%') AND ($3=0 OR l.status>=$3) AND ($4=0 OR l.user_id=$4)
ORDER BY l.id DESC LIMIT $5`, method, path, statusMin, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id, uid, dur int64
		var method, path, errorCode, requestID, ip, ua string
		var status int
		var apiToken bool
		var created time.Time
		if err := rows.Scan(&id, &method, &path, &status, &uid, &apiToken, &errorCode, &requestID, &ip, &ua, &dur, &created); err != nil {
			return nil, err
		}
		out = append(out, map[string]any{"id": id, "method": method, "path": path, "status": status, "user_id": uid, "api_token": apiToken, "error_code": errorCode, "request_id": requestID, "ip": ip, "user_agent": ua, "duration_ms": dur, "created_at": created})
	}
	return out, rows.Err()
}

// ---- device / session management ----

// ListSessions returns the caller's active sessions, flagging the current one.
func (s *Store) ListSessions(ctx context.Context, userID int64, currentTokenHash string) ([]model.SessionDevice, error) {
	rows, err := s.DB.Query(ctx, `SELECT id,coalesce(ip::text,''),coalesce(user_agent,''),created_at,last_seen_at,expires_at,(token_hash=$2) FROM user_sessions
WHERE user_id=$1 AND revoked_at IS NULL AND expires_at>now() ORDER BY last_seen_at DESC LIMIT 50`, userID, currentTokenHash)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.SessionDevice{}
	for rows.Next() {
		var v model.SessionDevice
		if err := rows.Scan(&v.ID, &v.IP, &v.UserAgent, &v.CreatedAt, &v.LastSeen, &v.ExpiresAt, &v.Current); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// RevokeSessionByID kills one of the caller's sessions (object-level check
// inside the statement: user_id must match, §10 IDOR).
func (s *Store) RevokeSessionByID(ctx context.Context, userID, sessionID int64) error {
	tag, err := s.DB.Exec(ctx, `UPDATE user_sessions SET revoked_at=now() WHERE id=$1 AND user_id=$2 AND revoked_at IS NULL`, sessionID, userID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// RevokeOtherSessions logs out every device except the current one.
func (s *Store) RevokeOtherSessions(ctx context.Context, userID int64, keepTokenHash string) (int64, error) {
	tag, err := s.DB.Exec(ctx, `UPDATE user_sessions SET revoked_at=now() WHERE user_id=$1 AND token_hash<>$2 AND revoked_at IS NULL`, userID, keepTokenHash)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// ---- TOTP 2FA (§9) ----

// SaveTOTPPending stores an encrypted secret without enabling it yet.
func (s *Store) SaveTOTPPending(ctx context.Context, userID int64, secretEncrypted string) error {
	_, err := s.DB.Exec(ctx, `UPDATE user_security SET totp_secret_encrypted=$2 WHERE user_id=$1`, userID, secretEncrypted)
	return err
}

// EnableTOTP marks the pending secret as active.
func (s *Store) EnableTOTP(ctx context.Context, userID int64) error {
	tag, err := s.DB.Exec(ctx, `UPDATE user_security SET totp_enabled_at=now() WHERE user_id=$1 AND totp_secret_encrypted IS NOT NULL`, userID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrInvalidState
	}
	return nil
}

// DisableTOTP removes the second factor entirely.
func (s *Store) DisableTOTP(ctx context.Context, userID int64) error {
	tag, err := s.DB.Exec(ctx, `UPDATE user_security SET totp_secret_encrypted=NULL,totp_enabled_at=NULL WHERE user_id=$1 AND totp_enabled_at IS NOT NULL`, userID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrInvalidState
	}
	return nil
}

// HasSuccessLoginFromOtherIP drives 异常 IP 检测: did this account ever log
// in successfully from a different address before?
func (s *Store) HasSuccessLoginFromOtherIP(ctx context.Context, userID int64, ip string) (bool, error) {
	var exists bool
	err := s.DB.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM user_login_logs WHERE user_id=$1 AND success=TRUE AND created_at>now()-interval '90 days' AND ip IS DISTINCT FROM NULLIF($2,'')::inet)`, userID, ip).Scan(&exists)
	return exists, err
}

// ---- product groups (第五阶段) ----

// ListProductPrices returns every purchasable cycle of a product.
func (s *Store) ListProductPrices(ctx context.Context, productPublicID string) ([]model.ProductPrice, error) {
	rows, err := s.DB.Query(ctx, `SELECT pp.billing_cycle,pp.currency,pp.amount_cents,pp.active FROM product_prices pp JOIN products p ON p.id=pp.product_id WHERE p.public_id=$1 AND p.deleted_at IS NULL AND pp.active=true ORDER BY pp.amount_cents`, productPublicID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.ProductPrice{}
	for rows.Next() {
		var v model.ProductPrice
		if err := rows.Scan(&v.BillingCycle, &v.Currency, &v.AmountCents, &v.Active); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// TOTPState reports whether the caller has the second factor enabled.
func (s *Store) TOTPState(ctx context.Context, userID int64) (bool, error) {
	var enabled bool
	err := s.DB.QueryRow(ctx, `SELECT totp_enabled_at IS NOT NULL FROM user_security WHERE user_id=$1`, userID).Scan(&enabled)
	return enabled, err
}

func (s *Store) ListProductGroups(ctx context.Context, withCount bool) ([]model.ProductGroup, error) {
	q := `SELECT g.public_id::text,g.name,g.sort_weight,g.created_at`
	if withCount {
		q += `,count(p.id)`
	} else {
		q += `,0`
	}
	q += ` FROM product_groups g LEFT JOIN products p ON p.group_id=g.id AND p.deleted_at IS NULL AND p.active=true
GROUP BY g.id ORDER BY g.sort_weight DESC, g.id ASC`
	rows, err := s.DB.Query(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.ProductGroup{}
	for rows.Next() {
		var v model.ProductGroup
		if err := rows.Scan(&v.PublicID, &v.Name, &v.SortWeight, &v.CreatedAt, &v.ProductCount); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Store) CreateProductGroup(ctx context.Context, name string, sortWeight int) (model.ProductGroup, error) {
	var v model.ProductGroup
	err := s.DB.QueryRow(ctx, `INSERT INTO product_groups(name,sort_weight) VALUES($1,$2) RETURNING public_id::text,name,sort_weight,0,created_at`, name, sortWeight).Scan(&v.PublicID, &v.Name, &v.SortWeight, &v.ProductCount, &v.CreatedAt)
	if err != nil {
		if isUniqueViolation(err) {
			return model.ProductGroup{}, fmtErrDuplicate("分组名称已存在")
		}
		return model.ProductGroup{}, err
	}
	return v, nil
}

func (s *Store) UpdateProductGroup(ctx context.Context, publicID, name string, sortWeight int) (model.ProductGroup, error) {
	var v model.ProductGroup
	err := s.DB.QueryRow(ctx, `UPDATE product_groups SET name=$2,sort_weight=$3 WHERE public_id=$1 RETURNING public_id::text,name,sort_weight,0,created_at`, publicID, name, sortWeight).Scan(&v.PublicID, &v.Name, &v.SortWeight, &v.ProductCount, &v.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.ProductGroup{}, ErrNotFound
	}
	if err != nil {
		if isUniqueViolation(err) {
			return model.ProductGroup{}, fmtErrDuplicate("分组名称已存在")
		}
		return model.ProductGroup{}, err
	}
	return v, nil
}

// DeleteProductGroup detaches its products (FK ON DELETE SET NULL).
func (s *Store) DeleteProductGroup(ctx context.Context, publicID string) error {
	tag, err := s.DB.Exec(ctx, `DELETE FROM product_groups WHERE public_id=$1`, publicID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ---- product administration (edit / list-all / on-off shelf) ----

// ListProductsAdmin returns every non-deleted product including inactive ones.
func (s *Store) ListProductsAdmin(ctx context.Context) ([]model.Product, error) {
	rows, err := s.DB.Query(ctx, `SELECT p.id,p.public_id::text,p.name,p.description,coalesce(pr.public_id::text,''),coalesce(pr.name,''),p.provider_type,p.active,pp.amount_cents,pp.currency,pp.billing_cycle,coalesce(g.public_id::text,''),coalesce(g.name,''),p.created_at,
p.pay_type,p.trial_days,p.trial_price_cents,p.auto_terminate_days,
p.stock_control,p.stock_qty,p.sold_count,p.allow_qty,p.max_per_customer,p.is_featured
FROM products p
JOIN product_prices pp ON pp.product_id=p.id AND pp.active=true
LEFT JOIN providers pr ON pr.id=p.provider_id
LEFT JOIN product_groups g ON g.id=p.group_id
WHERE p.deleted_at IS NULL
ORDER BY p.id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Product{}
	for rows.Next() {
		var v model.Product
		if err := rows.Scan(&v.ID, &v.PublicID, &v.Name, &v.Description, &v.ProviderID, &v.ProviderName, &v.ProviderType, &v.Active, &v.PriceCents, &v.Currency, &v.BillingCycle, &v.GroupID, &v.GroupName, &v.CreatedAt,
			&v.PayType, &v.TrialDays, &v.TrialPriceCents, &v.AutoTerminateDays,
			&v.StockControl, &v.StockQty, &v.SoldCount, &v.AllowQty, &v.MaxPerCustomer, &v.IsFeatured); err != nil {
			return nil, err
		}
		if v.StockControl {
			v.Available = v.StockQty - v.SoldCount
			if v.Available < 0 {
				v.Available = 0
			}
		} else {
			v.Available = -1
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// 附上每个商品的全部价格档，供后台编辑器回填多周期价格。
	for i := range out {
		prices, perr := s.ListProductPrices(ctx, out[i].PublicID)
		if perr == nil {
			out[i].Prices = prices
		}
	}
	return out, nil
}

// UpdateProduct edits display fields, shelf state, grouping and the price of
// the product's active billing cycle. Zero-value pointers keep the column.
func (s *Store) UpdateProduct(ctx context.Context, publicID, name, description string, groupPublicID *string, sortWeight *int, active *bool, amountCents *int64) (model.Product, error) {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return model.Product{}, err
	}
	defer tx.Rollback(ctx)
	var groupID *int64
	if groupPublicID != nil {
		if *groupPublicID == "" {
			groupID = nil
		} else {
			var id int64
			if err := tx.QueryRow(ctx, `SELECT id FROM product_groups WHERE public_id=$1`, *groupPublicID).Scan(&id); err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					return model.Product{}, ErrNotFound
				}
				return model.Product{}, err
			}
			groupID = &id
		}
	}
	var productID int64
	err = tx.QueryRow(ctx, `UPDATE products SET
name=CASE WHEN $2='' THEN name ELSE $2 END,
description=CASE WHEN $3='' THEN description ELSE $3 END,
group_id=CASE WHEN $4::text IS NULL THEN group_id WHEN $4::text='' THEN NULL ELSE (SELECT id FROM product_groups WHERE public_id=$4::text) END,
sort_weight=coalesce($5,sort_weight),
active=coalesce($6,active),
updated_at=now()
WHERE public_id=$1 AND deleted_at IS NULL RETURNING id`,
		publicID, name, description, groupPublicID, sortWeight, active).Scan(&productID)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.Product{}, ErrNotFound
	}
	if err != nil {
		return model.Product{}, err
	}
	if amountCents != nil && *amountCents >= 0 {
		if _, err := tx.Exec(ctx, `UPDATE product_prices SET amount_cents=$2 WHERE product_id=$1 AND active=true`, productID, *amountCents); err != nil {
			return model.Product{}, err
		}
	}
	var v model.Product
	err = tx.QueryRow(ctx, `SELECT p.id,p.public_id::text,p.name,p.description,coalesce(pr.public_id::text,''),coalesce(pr.name,''),p.provider_type,p.active,pp.amount_cents,pp.currency,pp.billing_cycle,p.created_at
FROM products p JOIN product_prices pp ON pp.product_id=p.id AND pp.active=true LEFT JOIN providers pr ON pr.id=p.provider_id
WHERE p.id=$1 ORDER BY pp.id LIMIT 1`, productID).Scan(&v.ID, &v.PublicID, &v.Name, &v.Description, &v.ProviderID, &v.ProviderName, &v.ProviderType, &v.Active, &v.PriceCents, &v.Currency, &v.BillingCycle, &v.CreatedAt)
	if err != nil {
		return model.Product{}, err
	}
	_ = groupID
	return v, tx.Commit(ctx)
}

// ---- announcements ----

// GetAnnouncement 读一条对用户可见的公告。
// active=FALSE 的公告对用户不存在（返回 ErrNotFound），避免下线内容还能被翻出来。
func (s *Store) GetAnnouncement(ctx context.Context, publicID string) (model.Announcement, error) {
	var v model.Announcement
	err := s.DB.QueryRow(ctx, `SELECT public_id::text,title,body,active,pinned,created_at,updated_at
FROM announcements WHERE public_id=$1 AND active=TRUE`, strings.TrimSpace(publicID)).
		Scan(&v.PublicID, &v.Title, &v.Body, &v.Active, &v.Pinned, &v.CreatedAt, &v.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return v, ErrNotFound
	}
	return v, err
}

func (s *Store) ListAnnouncements(ctx context.Context, activeOnly bool, limit int) ([]model.Announcement, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	where := ""
	if activeOnly {
		where = "WHERE active=TRUE"
	}
	rows, err := s.DB.Query(ctx, `SELECT public_id::text,title,body,active,pinned,created_at,updated_at FROM announcements `+where+` ORDER BY pinned DESC, created_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Announcement{}
	for rows.Next() {
		var v model.Announcement
		if err := rows.Scan(&v.PublicID, &v.Title, &v.Body, &v.Active, &v.Pinned, &v.CreatedAt, &v.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Store) CreateAnnouncement(ctx context.Context, title, body string, active, pinned bool) (model.Announcement, error) {
	var v model.Announcement
	err := s.DB.QueryRow(ctx, `INSERT INTO announcements(title,body,active,pinned) VALUES($1,$2,$3,$4) RETURNING public_id::text,title,body,active,pinned,created_at,updated_at`, title, body, active, pinned).Scan(&v.PublicID, &v.Title, &v.Body, &v.Active, &v.Pinned, &v.CreatedAt, &v.UpdatedAt)
	return v, err
}

func (s *Store) UpdateAnnouncement(ctx context.Context, publicID, title, body string, active, pinned bool) (model.Announcement, error) {
	var v model.Announcement
	err := s.DB.QueryRow(ctx, `UPDATE announcements SET title=$2,body=$3,active=$4,pinned=$5,updated_at=now() WHERE public_id=$1 RETURNING public_id::text,title,body,active,pinned,created_at,updated_at`, publicID, title, body, active, pinned).Scan(&v.PublicID, &v.Title, &v.Body, &v.Active, &v.Pinned, &v.CreatedAt, &v.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.Announcement{}, ErrNotFound
	}
	return v, err
}

func (s *Store) DeleteAnnouncement(ctx context.Context, publicID string) error {
	tag, err := s.DB.Exec(ctx, `DELETE FROM announcements WHERE public_id=$1`, publicID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ---- invoice detail ----

// GetInvoiceDetail returns one invoice owned by the user (§10 object-level
// authorization in the WHERE clause). Admins pass userID=0 to skip the check.
func (s *Store) GetInvoiceDetail(ctx context.Context, userID int64, publicID string) (model.InvoiceDetail, error) {
	var d model.InvoiceDetail
	ownerClause := "AND i.user_id=$2"
	args := []any{publicID, userID}
	if userID == 0 {
		ownerClause = "AND ($2=0 OR TRUE)"
	}
	err := s.DB.QueryRow(ctx, `SELECT i.public_id::text,o.public_id::text,i.status,i.total_cents,i.currency,i.due_at,i.created_at,i.paid_at,o.status
FROM invoices i JOIN orders o ON o.id=i.order_id WHERE i.public_id=$1 `+ownerClause, args...).Scan(&d.PublicID, &d.OrderID, &d.Status, &d.TotalCents, &d.Currency, &d.DueAt, &d.CreatedAt, &d.PaidAt, &d.OrderStatus)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.InvoiceDetail{}, ErrNotFound
	}
	if err != nil {
		return model.InvoiceDetail{}, err
	}
	rows, err := s.DB.Query(ctx, `SELECT description,amount_cents FROM invoice_items WHERE invoice_id=(SELECT id FROM invoices WHERE public_id=$1) ORDER BY id`, publicID)
	if err != nil {
		return model.InvoiceDetail{}, err
	}
	defer rows.Close()
	d.Items = []model.InvoiceItem{}
	for rows.Next() {
		var it model.InvoiceItem
		if err := rows.Scan(&it.Description, &it.AmountCents); err != nil {
			return model.InvoiceDetail{}, err
		}
		d.Items = append(d.Items, it)
	}
	return d, rows.Err()
}

// ---- service detail ----

// GetServiceDetail returns one service with its provisioned config. The owner
// clause enforces object-level authorization (§10); userID=0 means admin.
func (s *Store) GetServiceDetail(ctx context.Context, userID int64, publicID string) (model.ServiceDetail, error) {
	var d model.ServiceDetail
	var payload []byte
	ownerClause := "s.user_id=$2"
	args := []any{publicID, userID}
	if userID == 0 {
		ownerClause = "($2=0 OR s.user_id=$2)"
	}
	err := s.DB.QueryRow(ctx, `SELECT s.public_id::text,s.status,s.provider_type,coalesce(s.provider_ref,''),p.name,
coalesce(oi.billing_cycle,''),coalesce(oi.unit_price_cents,0),coalesce(o.currency,'CNY'),s.expires_at,s.created_at,s.provider_payload
FROM services s
JOIN products p ON p.id=s.product_id
LEFT JOIN order_items oi ON oi.id=s.order_item_id
LEFT JOIN orders o ON o.id=s.order_id
WHERE s.public_id=$1 AND `+ownerClause, args...).Scan(&d.PublicID, &d.Status, &d.ProviderType, &d.ProviderRef, &d.ProductName, &d.BillingCycle, &d.PriceCents, &d.Currency, &d.ExpiresAt, &d.CreatedAt, &payload)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.ServiceDetail{}, ErrNotFound
	}
	if err != nil {
		return model.ServiceDetail{}, err
	}
	if len(payload) > 0 {
		_ = jsonUnmarshal(payload, &d.Config)
	}
	return d, nil
}
