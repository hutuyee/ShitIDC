package store

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/hutuyee/ShitIDC/internal/model"
)

var ErrTicketClosed = errors.New("ticket closed")

// ListTickets returns the caller's own tickets (用户侧).
func (s *Store) ListTickets(ctx context.Context, userID int64) ([]model.Ticket, error) {
	rows, err := s.DB.Query(ctx, `SELECT public_id::text,subject,status,priority,last_reply_at,last_reply_is_staff,created_at
FROM tickets WHERE user_id=$1 ORDER BY COALESCE(last_reply_at,created_at) DESC LIMIT 200`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Ticket{}
	for rows.Next() {
		var t model.Ticket
		if err := rows.Scan(&t.PublicID, &t.Subject, &t.Status, &t.Priority, &t.LastReplyAt, &t.LastReplyIsStaff, &t.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// ListAllTickets returns every ticket for the staff console, newest activity
// first, optionally filtered by status.
func (s *Store) ListAllTickets(ctx context.Context, status string, limit int) ([]model.Ticket, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	status = strings.TrimSpace(status)
	rows, err := s.DB.Query(ctx, `SELECT t.public_id::text,t.subject,t.status,t.priority,t.user_id,u.email,
t.last_reply_at,t.last_reply_is_staff,t.created_at
FROM tickets t JOIN users u ON u.id=t.user_id
WHERE ($1='' OR t.status=$1)
ORDER BY COALESCE(t.last_reply_at,t.created_at) DESC LIMIT $2`, status, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Ticket{}
	for rows.Next() {
		var t model.Ticket
		if err := rows.Scan(&t.PublicID, &t.Subject, &t.Status, &t.Priority, &t.UserUID, &t.UserEmail, &t.LastReplyAt, &t.LastReplyIsStaff, &t.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// GetTicket loads one ticket with its full conversation. userID <= 0 (staff)
// skips the ownership check; otherwise the ticket must belong to the caller.
func (s *Store) GetTicket(ctx context.Context, ticketPublicID string, userID int64) (model.TicketDetail, error) {
	var d model.TicketDetail
	var ownerID int64
	err := s.DB.QueryRow(ctx, `SELECT public_id::text,subject,status,priority,user_id,last_reply_at,last_reply_is_staff,created_at
FROM tickets WHERE public_id=$1`, ticketPublicID).
		Scan(&d.PublicID, &d.Subject, &d.Status, &d.Priority, &ownerID, &d.LastReplyAt, &d.LastReplyIsStaff, &d.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.TicketDetail{}, ErrNotFound
	}
	if err != nil {
		return model.TicketDetail{}, err
	}
	if userID > 0 && ownerID != userID {
		return model.TicketDetail{}, ErrNotFound
	}
	d.UserUID = ownerID
	var email string
	if err := s.DB.QueryRow(ctx, `SELECT email FROM users WHERE id=$1`, ownerID).Scan(&email); err == nil {
		d.UserEmail = email
	}
	rows, err := s.DB.Query(ctx, `SELECT m.id::text,coalesce(m.user_id,0),coalesce(u.email,''),m.is_staff,m.body,m.created_at
FROM ticket_messages m LEFT JOIN users u ON u.id=m.user_id
WHERE m.ticket_id=(SELECT id FROM tickets WHERE public_id=$1) ORDER BY m.created_at ASC, m.id ASC`, ticketPublicID)
	if err != nil {
		return model.TicketDetail{}, err
	}
	defer rows.Close()
	d.Messages = []model.TicketMessage{}
	for rows.Next() {
		var m model.TicketMessage
		if err := rows.Scan(&m.PublicID, &m.SenderUID, &m.SenderEmail, &m.IsStaff, &m.Body, &m.CreatedAt); err != nil {
			return model.TicketDetail{}, err
		}
		d.Messages = append(d.Messages, m)
	}
	if err := rows.Err(); err != nil {
		return model.TicketDetail{}, err
	}
	return d, nil
}

// ReplyTicket appends a message to a ticket. User replies reopen closed
// tickets; staff replies move the ticket to 'pending' (waiting on the user).
// Returns the created message and the ticket owner id (for notifications).
func (s *Store) ReplyTicket(ctx context.Context, ticketPublicID string, senderID int64, isStaff bool, body string) (model.TicketMessage, int64, error) {
	body = strings.TrimSpace(body)
	if body == "" {
		return model.TicketMessage{}, 0, errors.New("回复内容不能为空")
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
	newStatus := status
	if isStaff {
		newStatus = "pending" // staff answered, waiting for the user
	} else if status == "closed" {
		newStatus = "open" // user reply reopens the ticket
	} else {
		newStatus = "open"
	}
	var m model.TicketMessage
	m.SenderUID = senderID
	m.IsStaff = isStaff
	m.Body = body
	if err := tx.QueryRow(ctx, `INSERT INTO ticket_messages(ticket_id,user_id,is_staff,body) VALUES($1,$2,$3,$4)
RETURNING id::text,created_at`, ticketID, senderID, isStaff, body).Scan(&m.PublicID, &m.CreatedAt); err != nil {
		return model.TicketMessage{}, 0, err
	}
	if _, err := tx.Exec(ctx, `UPDATE tickets SET status=$2,last_reply_at=now(),last_reply_is_staff=$3,updated_at=now() WHERE id=$1`, ticketID, newStatus, isStaff); err != nil {
		return model.TicketMessage{}, 0, err
	}
	if err := s.DB.QueryRow(ctx, `SELECT email FROM users WHERE id=$1`, senderID).Scan(&m.SenderEmail); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return model.TicketMessage{}, 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return model.TicketMessage{}, 0, err
	}
	return m, ownerID, nil
}

// SetTicketStatus transitions a ticket. Users may only close/reopen their own;
// staff can set any supported status on any ticket.
func (s *Store) SetTicketStatus(ctx context.Context, ticketPublicID string, userID int64, isStaff bool, status string) (string, error) {
	switch status {
	case "open", "pending", "closed":
	default:
		return "", ErrInvalidState
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)
	var ticketID, ownerID int64
	err = tx.QueryRow(ctx, `SELECT id,user_id FROM tickets WHERE public_id=$1 FOR UPDATE`, ticketPublicID).Scan(&ticketID, &ownerID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	if !isStaff && ownerID != userID {
		return "", ErrNotFound
	}
	// Only staff may force 'pending'; users toggle open/closed.
	if !isStaff && status == "pending" {
		return "", ErrInvalidState
	}
	if _, err := tx.Exec(ctx, `UPDATE tickets SET status=$2,updated_at=now() WHERE id=$1`, ticketID, status); err != nil {
		return "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return "", err
	}
	return status, nil
}

// TicketStaffEmails returns the mailboxes of active users holding
// ticket.manage, used to notify staff when a user writes in.
func (s *Store) TicketStaffEmails(ctx context.Context) ([]string, error) {
	rows, err := s.DB.Query(ctx, `SELECT DISTINCT u.email FROM users u
JOIN user_roles ur ON ur.user_id=u.id
JOIN role_permissions rp ON rp.role_id=ur.role_id
JOIN permissions p ON p.id=rp.permission_id AND p.name='ticket.manage'
WHERE u.status='active' AND u.deleted_at IS NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var email string
		if err := rows.Scan(&email); err != nil {
			return nil, err
		}
		out = append(out, email)
	}
	return out, rows.Err()
}

// CountOpenTickets is a small helper for dashboards.
func (s *Store) CountOpenTickets(ctx context.Context) (int64, error) {
	var n int64
	err := s.DB.QueryRow(ctx, `SELECT count(*) FROM tickets WHERE status IN ('open','pending')`).Scan(&n)
	return n, err
}
