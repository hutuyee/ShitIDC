package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/hutuyee/ShitIDC/internal/model"
)

// V2 store queries part 2: notifications, ticket attachments, mail
// templates, currencies and the WASM extension registry.

// ---- notifications (站内通知中心) ----

func (s *Store) InsertNotification(ctx context.Context, userID int64, typ, title, body, link string) error {
	_, err := s.DB.Exec(ctx, `INSERT INTO notifications(user_id,type,title,body,link) VALUES($1,$2,$3,$4,NULLIF($5,''))`, userID, typ, title, body, link)
	return err
}

func (s *Store) ListNotifications(ctx context.Context, userID int64, limit int) ([]model.Notification, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	rows, err := s.DB.Query(ctx, `SELECT public_id::text,type,title,body,coalesce(link,''),read_at,created_at FROM notifications WHERE user_id=$1 ORDER BY id DESC LIMIT $2`, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Notification{}
	for rows.Next() {
		var v model.Notification
		if err := rows.Scan(&v.PublicID, &v.Type, &v.Title, &v.Body, &v.Link, &v.ReadAt, &v.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Store) UnreadNotificationCount(ctx context.Context, userID int64) (int64, error) {
	var n int64
	err := s.DB.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE user_id=$1 AND read_at IS NULL`, userID).Scan(&n)
	return n, err
}

func (s *Store) MarkNotificationRead(ctx context.Context, userID int64, publicID string) error {
	if publicID == "all" {
		_, err := s.DB.Exec(ctx, `UPDATE notifications SET read_at=now() WHERE user_id=$1 AND read_at IS NULL`, userID)
		return err
	}
	tag, err := s.DB.Exec(ctx, `UPDATE notifications SET read_at=now() WHERE user_id=$1 AND public_id=$2 AND read_at IS NULL`, userID, publicID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ---- ticket attachments (工单附件, §43) ----

func (s *Store) InsertAttachment(ctx context.Context, ticketPublicID string, uploaderID int64, filename, storedPath, mime string, size int64) (model.TicketAttachment, error) {
	var v model.TicketAttachment
	var created time.Time
	err := s.DB.QueryRow(ctx, `INSERT INTO ticket_attachments(ticket_id,uploader_id,filename,stored_path,mime,size_bytes)
VALUES((SELECT id FROM tickets WHERE public_id=$1),$2,$3,$4,$5,$6) RETURNING public_id::text,created_at`,
		ticketPublicID, uploaderID, filename, storedPath, mime, size).Scan(&v.PublicID, &created)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.TicketAttachment{}, ErrNotFound
	}
	if err != nil {
		return model.TicketAttachment{}, err
	}
	v.TicketID, v.UploaderID, v.Filename, v.Mime, v.SizeBytes, v.CreatedAt = ticketPublicID, uploaderID, filename, mime, size, created
	return v, nil
}

func (s *Store) ListTicketAttachments(ctx context.Context, ticketPublicID string) ([]model.TicketAttachment, error) {
	rows, err := s.DB.Query(ctx, `SELECT a.public_id::text,t.public_id::text,a.uploader_id,a.filename,a.mime,a.size_bytes,a.created_at
FROM ticket_attachments a JOIN tickets t ON t.id=a.ticket_id WHERE t.public_id=$1 ORDER BY a.id`, ticketPublicID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.TicketAttachment{}
	for rows.Next() {
		var v model.TicketAttachment
		if err := rows.Scan(&v.PublicID, &v.TicketID, &v.UploaderID, &v.Filename, &v.Mime, &v.SizeBytes, &v.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// GetAttachmentForRead enforces object-level authorization (§10): only the
// ticket owner or staff (userID=0) may download.
func (s *Store) GetAttachmentForRead(ctx context.Context, userID int64, attachmentPublicID string) (model.TicketAttachment, string, error) {
	var v model.TicketAttachment
	var path string
	var owner int64
	err := s.DB.QueryRow(ctx, `SELECT a.public_id::text,t.public_id::text,a.uploader_id,a.filename,a.mime,a.size_bytes,a.created_at,a.stored_path,t.user_id
FROM ticket_attachments a JOIN tickets t ON t.id=a.ticket_id WHERE a.public_id=$1`, attachmentPublicID).Scan(&v.PublicID, &v.TicketID, &v.UploaderID, &v.Filename, &v.Mime, &v.SizeBytes, &v.CreatedAt, &path, &owner)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.TicketAttachment{}, "", ErrNotFound
	}
	if err != nil {
		return model.TicketAttachment{}, "", err
	}
	if userID != 0 && owner != userID {
		return model.TicketAttachment{}, "", ErrNotFound
	}
	return v, path, nil
}

// ---- mail templates (邮件模板) ----

func (s *Store) ListMailTemplates(ctx context.Context) ([]model.MailTemplate, error) {
	rows, err := s.DB.Query(ctx, `SELECT name,subject,body,updated_at FROM mail_templates ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.MailTemplate{}
	for rows.Next() {
		var v model.MailTemplate
		if err := rows.Scan(&v.Name, &v.Subject, &v.Body, &v.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Store) GetMailTemplate(ctx context.Context, name string) (model.MailTemplate, error) {
	var v model.MailTemplate
	err := s.DB.QueryRow(ctx, `SELECT name,subject,body,updated_at FROM mail_templates WHERE name=$1`, name).Scan(&v.Name, &v.Subject, &v.Body, &v.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.MailTemplate{}, ErrNotFound
	}
	return v, err
}

func (s *Store) SaveMailTemplate(ctx context.Context, name, subject, body string) error {
	tag, err := s.DB.Exec(ctx, `INSERT INTO mail_templates(name,subject,body) VALUES($1,$2,$3)
ON CONFLICT (name) DO UPDATE SET subject=excluded.subject,body=excluded.body,updated_at=now()`, name, subject, body)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) DeleteMailTemplate(ctx context.Context, name string) error {
	tag, err := s.DB.Exec(ctx, `DELETE FROM mail_templates WHERE name=$1`, name)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ---- currencies (多币种) ----

func (s *Store) ListCurrencies(ctx context.Context, activeOnly bool) ([]model.Currency, error) {
	where := ""
	if activeOnly {
		where = "WHERE active=TRUE"
	}
	rows, err := s.DB.Query(ctx, `SELECT code,rate::text,symbol,active FROM currencies `+where+` ORDER BY code`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Currency{}
	for rows.Next() {
		var v model.Currency
		if err := rows.Scan(&v.Code, &v.Rate, &v.Symbol, &v.Active); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Store) SaveCurrency(ctx context.Context, code, rate, symbol string, active bool) error {
	tag, err := s.DB.Exec(ctx, `INSERT INTO currencies(code,rate,symbol,active) VALUES(upper($1),$2,$3,$4)
ON CONFLICT (code) DO UPDATE SET rate=excluded.rate,symbol=excluded.symbol,active=excluded.active`, code, rate, symbol, active)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) DeleteCurrency(ctx context.Context, code string) error {
	if strings.EqualFold(code, "CNY") {
		return fmt.Errorf("基础货币不可删除")
	}
	tag, err := s.DB.Exec(ctx, `DELETE FROM currencies WHERE code=upper($1)`, code)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ---- WASM extension registry (第十阶段) ----

func (s *Store) ListExtensions(ctx context.Context) ([]model.Extension, error) {
	rows, err := s.DB.Query(ctx, `SELECT public_id::text,name,version,description,permissions,active,source_path,created_at,updated_at FROM extensions ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Extension{}
	for rows.Next() {
		var v model.Extension
		if err := rows.Scan(&v.PublicID, &v.Name, &v.Version, &v.Description, &v.Permissions, &v.Active, &v.SourcePath, &v.CreatedAt, &v.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// UpsertExtension registers an uploaded extension package (manifest wins).
func (s *Store) UpsertExtension(ctx context.Context, name, version, description, sourcePath string, permissions []string, active bool) (model.Extension, error) {
	var v model.Extension
	err := s.DB.QueryRow(ctx, `INSERT INTO extensions(name,version,description,permissions,active,source_path) VALUES($1,$2,$3,$4,$5,$6)
ON CONFLICT (name) DO UPDATE SET version=excluded.version,description=excluded.description,permissions=excluded.permissions,source_path=excluded.source_path,updated_at=now()
RETURNING public_id::text,name,version,description,permissions,active,source_path,created_at,updated_at`,
		name, version, description, permissions, active, sourcePath).Scan(&v.PublicID, &v.Name, &v.Version, &v.Description, &v.Permissions, &v.Active, &v.SourcePath, &v.CreatedAt, &v.UpdatedAt)
	return v, err
}

func (s *Store) SetExtensionActive(ctx context.Context, publicID string, active bool) (model.Extension, error) {
	var v model.Extension
	err := s.DB.QueryRow(ctx, `UPDATE extensions SET active=$2,updated_at=now() WHERE public_id=$1
RETURNING public_id::text,name,version,description,permissions,active,source_path,created_at,updated_at`, publicID, active).Scan(&v.PublicID, &v.Name, &v.Version, &v.Description, &v.Permissions, &v.Active, &v.SourcePath, &v.CreatedAt, &v.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.Extension{}, ErrNotFound
	}
	return v, err
}

func (s *Store) DeleteExtension(ctx context.Context, publicID string) (model.Extension, error) {
	var v model.Extension
	err := s.DB.QueryRow(ctx, `DELETE FROM extensions WHERE public_id=$1
RETURNING public_id::text,name,version,description,permissions,active,source_path,created_at,updated_at`, publicID).Scan(&v.PublicID, &v.Name, &v.Version, &v.Description, &v.Permissions, &v.Active, &v.SourcePath, &v.CreatedAt, &v.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.Extension{}, ErrNotFound
	}
	return v, err
}

// ActiveExtensions loads the enabled set for the dispatcher at startup.
func (s *Store) ActiveExtensions(ctx context.Context) ([]model.Extension, error) {
	rows, err := s.DB.Query(ctx, `SELECT public_id::text,name,version,description,permissions,active,source_path,created_at,updated_at FROM extensions WHERE active=TRUE ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Extension{}
	for rows.Next() {
		var v model.Extension
		if err := rows.Scan(&v.PublicID, &v.Name, &v.Version, &v.Description, &v.Permissions, &v.Active, &v.SourcePath, &v.CreatedAt, &v.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// ExtStorageGet / ExtStorageSet back the extension kv capability.
func (s *Store) ExtStorageGet(ctx context.Context, extension, key string) ([]byte, bool, error) {
	var value []byte
	err := s.DB.QueryRow(ctx, `SELECT value FROM ext_storage WHERE extension=$1 AND key=$2`, extension, key).Scan(&value)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	return value, err == nil, err
}

func (s *Store) ExtStorageSet(ctx context.Context, extension, key string, value []byte) error {
	_, err := s.DB.Exec(ctx, `INSERT INTO ext_storage(extension,key,value) VALUES($1,$2,$3)
ON CONFLICT (extension,key) DO UPDATE SET value=excluded.value,updated_at=now()`, extension, key, value)
	return err
}
