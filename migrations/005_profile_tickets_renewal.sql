-- Self-service user profile, staff-ticket workflow (permission-gated),
-- reply tracking for tickets and renewable orders.
-- Safe for an existing ShitIDC database: every schema addition is guarded.

-- 1. User profile (个人中心自定义资料)
CREATE TABLE IF NOT EXISTS user_profiles (
    user_id BIGINT PRIMARY KEY REFERENCES users(id) ON DELETE RESTRICT,
    nickname TEXT NOT NULL DEFAULT '',
    real_name TEXT NOT NULL DEFAULT '',
    company TEXT NOT NULL DEFAULT '',
    phone TEXT NOT NULL DEFAULT '',
    qq TEXT NOT NULL DEFAULT '',
    country TEXT NOT NULL DEFAULT '中国',
    province TEXT NOT NULL DEFAULT '',
    city TEXT NOT NULL DEFAULT '',
    address TEXT NOT NULL DEFAULT '',
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- 2. Ticket conversation tracking: when the last reply happened and by whom,
--    so staff and users can see which side is expected to act next.
ALTER TABLE tickets ADD COLUMN IF NOT EXISTS last_reply_at TIMESTAMPTZ;
ALTER TABLE tickets ADD COLUMN IF NOT EXISTS last_reply_is_staff BOOLEAN NOT NULL DEFAULT FALSE;
CREATE INDEX IF NOT EXISTS idx_tickets_status_updated ON tickets(status, updated_at DESC);
CREATE INDEX IF NOT EXISTS idx_ticket_messages_ticket ON ticket_messages(ticket_id, created_at);

-- Backfill existing tickets from their message history.
UPDATE tickets t SET last_reply_at = m.last_at, last_reply_is_staff = m.last_is_staff
FROM (
    SELECT DISTINCT ON (ticket_id) ticket_id, created_at AS last_at, is_staff AS last_is_staff
    FROM ticket_messages ORDER BY ticket_id, created_at DESC, id DESC
) m
WHERE t.id = m.ticket_id AND t.last_reply_at IS NULL;

-- 3. Order kind: 'new' (provision a fresh service) vs 'renewal'
--    (extend an existing service's expiry on payment, no new service row).
ALTER TABLE orders ADD COLUMN IF NOT EXISTS kind TEXT NOT NULL DEFAULT 'new';
DO $$ BEGIN
    ALTER TABLE orders ADD CONSTRAINT orders_kind_check CHECK (kind IN ('new','renewal'));
EXCEPTION WHEN duplicate_object THEN NULL; END $$;
ALTER TABLE orders ADD COLUMN IF NOT EXISTS renew_service_id BIGINT REFERENCES services(id) ON DELETE SET NULL;

-- 4. Permissions: staff ticket console is reachable without full admin,
--    profile management is a normal self-service scope for API tokens.
INSERT INTO permissions(name, description) VALUES
('ticket.manage','查看并回复所有用户工单（客服）'),
('profile.manage','管理个人资料')
ON CONFLICT(name) DO NOTHING;

-- support gets the ticket console (NOT wallet.adjust / provider.manage / product.write).
INSERT INTO role_permissions(role_id, permission_id)
SELECT r.id,p.id FROM roles r, permissions p
WHERE r.name='support' AND p.name='ticket.manage'
ON CONFLICT DO NOTHING;

-- admin always receives every permission, including newly introduced ones.
INSERT INTO role_permissions(role_id, permission_id)
SELECT r.id,p.id FROM roles r, permissions p
WHERE r.name='admin'
ON CONFLICT DO NOTHING;

-- every role keeps self-service profile management.
INSERT INTO role_permissions(role_id, permission_id)
SELECT r.id,p.id FROM roles r, permissions p
WHERE p.name='profile.manage' AND r.name IN ('customer','support','finance','admin')
ON CONFLICT DO NOTHING;
