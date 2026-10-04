-- V2 feature set: coupons, referral commissions, agent user groups,
-- notifications, ticket attachments, mail templates, currencies, gateway
-- refunds and the WASM extension registry (路线图 §69 第二版 + §70 可落地项).
-- Safe for an existing ShitIDC database: every schema addition is guarded.

-- 1. Coupons (优惠系统).
CREATE TABLE IF NOT EXISTS coupons (
    id BIGSERIAL PRIMARY KEY,
    public_id UUID NOT NULL DEFAULT gen_random_uuid() UNIQUE,
    code TEXT NOT NULL UNIQUE,
    type TEXT NOT NULL CHECK (type IN ('fixed','percent')),
    value BIGINT NOT NULL CHECK (value > 0),          -- fixed: cents off; percent: 1..100
    max_uses INTEGER,                                 -- NULL = unlimited
    max_uses_per_user INTEGER NOT NULL DEFAULT 1,
    used_count INTEGER NOT NULL DEFAULT 0,
    min_amount_cents BIGINT NOT NULL DEFAULT 0,
    product_ids UUID[] NOT NULL DEFAULT '{}',         -- empty = all products
    starts_at TIMESTAMPTZ,
    expires_at TIMESTAMPTZ,
    active BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS coupon_redemptions (
    id BIGSERIAL PRIMARY KEY,
    coupon_id BIGINT NOT NULL REFERENCES coupons(id) ON DELETE CASCADE,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    order_id BIGINT NOT NULL REFERENCES orders(id) ON DELETE RESTRICT,
    discount_cents BIGINT NOT NULL CHECK (discount_cents >= 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE(coupon_id, order_id)
);
CREATE INDEX IF NOT EXISTS idx_coupon_redemptions_user ON coupon_redemptions(user_id, created_at DESC);

ALTER TABLE orders ADD COLUMN IF NOT EXISTS discount_cents BIGINT NOT NULL DEFAULT 0;
ALTER TABLE orders ADD COLUMN IF NOT EXISTS coupon_code TEXT NOT NULL DEFAULT '';

-- 2. Referral / affiliate (推广系统).
ALTER TABLE users ADD COLUMN IF NOT EXISTS referred_by BIGINT REFERENCES users(id) ON DELETE SET NULL;
ALTER TABLE users ADD COLUMN IF NOT EXISTS referral_code TEXT UNIQUE;

CREATE TABLE IF NOT EXISTS referral_commissions (
    id BIGSERIAL PRIMARY KEY,
    public_id UUID NOT NULL DEFAULT gen_random_uuid() UNIQUE,
    referrer_id BIGINT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    referee_id BIGINT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    order_id BIGINT NOT NULL REFERENCES orders(id) ON DELETE RESTRICT,
    amount_cents BIGINT NOT NULL CHECK (amount_cents >= 0),
    currency CHAR(3) NOT NULL,
    idempotency_key TEXT UNIQUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_referral_referrer ON referral_commissions(referrer_id, created_at DESC);

-- 3. Agent / reseller user groups (代理系统): group discount on orders.
CREATE TABLE IF NOT EXISTS user_groups (
    id BIGSERIAL PRIMARY KEY,
    public_id UUID NOT NULL DEFAULT gen_random_uuid() UNIQUE,
    name TEXT NOT NULL UNIQUE,
    discount_percent INTEGER NOT NULL DEFAULT 0 CHECK (discount_percent BETWEEN 0 AND 100),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
ALTER TABLE users ADD COLUMN IF NOT EXISTS user_group_id BIGINT REFERENCES user_groups(id) ON DELETE SET NULL;

-- 4. Notification center (站内通知).
CREATE TABLE IF NOT EXISTS notifications (
    id BIGSERIAL PRIMARY KEY,
    public_id UUID NOT NULL DEFAULT gen_random_uuid() UNIQUE,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    type TEXT NOT NULL,
    title TEXT NOT NULL,
    body TEXT NOT NULL DEFAULT '',
    link TEXT NOT NULL DEFAULT '',
    read_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_notifications_user ON notifications(user_id, created_at DESC);

-- 5. Ticket attachments (工单附件, §43 上传安全).
CREATE TABLE IF NOT EXISTS ticket_attachments (
    id BIGSERIAL PRIMARY KEY,
    public_id UUID NOT NULL DEFAULT gen_random_uuid() UNIQUE,
    ticket_id BIGINT NOT NULL REFERENCES tickets(id) ON DELETE CASCADE,
    uploader_id BIGINT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    filename TEXT NOT NULL,
    stored_path TEXT NOT NULL,
    mime TEXT NOT NULL,
    size_bytes BIGINT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_ticket_attachments_ticket ON ticket_attachments(ticket_id);

-- 6. Editable mail templates (邮件模板). Name values: email_verification,
--    password_reset, ticket_user, ticket_staff, login_notify.
CREATE TABLE IF NOT EXISTS mail_templates (
    name TEXT PRIMARY KEY,
    subject TEXT NOT NULL,
    body TEXT NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- 7. Currencies (多币种). rate = units of this currency per 1 base currency.
CREATE TABLE IF NOT EXISTS currencies (
    code CHAR(3) PRIMARY KEY,
    rate NUMERIC(20,8) NOT NULL DEFAULT 1 CHECK (rate > 0),
    symbol TEXT NOT NULL DEFAULT '',
    active BOOLEAN NOT NULL DEFAULT TRUE
);

INSERT INTO currencies(code, rate, symbol, active) VALUES
('CNY', 1, '¥', TRUE)
ON CONFLICT (code) DO NOTHING;

-- 8. Gateway refunds (自动退款): which gateway executed the refund and how.
ALTER TABLE refunds ADD COLUMN IF NOT EXISTS gateway_refund_id TEXT NOT NULL DEFAULT '';
ALTER TABLE refunds ADD COLUMN IF NOT EXISTS status TEXT NOT NULL DEFAULT 'completed';

-- 9. WASM extension registry (Extension 系统, 第十阶段/§24).
CREATE TABLE IF NOT EXISTS extensions (
    id BIGSERIAL PRIMARY KEY,
    public_id UUID NOT NULL DEFAULT gen_random_uuid() UNIQUE,
    name TEXT NOT NULL UNIQUE,
    version TEXT NOT NULL DEFAULT '1.0.0',
    description TEXT NOT NULL DEFAULT '',
    permissions TEXT[] NOT NULL DEFAULT '{}',
    active BOOLEAN NOT NULL DEFAULT FALSE,
    source_path TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS ext_storage (
    extension TEXT NOT NULL,
    key TEXT NOT NULL,
    value BYTEA NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (extension, key)
);

-- 10. Permissions for the new consoles.
INSERT INTO permissions(name, description) VALUES
('coupon.manage','创建/管理优惠券与查看核销记录'),
('agent.manage','管理代理用户组与折扣'),
('extension.manage','上传/启停 WASM 扩展包'),
('theme.manage','上传/切换主题包'),
('finance.report','查看财务统计报表')
ON CONFLICT(name) DO NOTHING;

INSERT INTO role_permissions(role_id, permission_id)
SELECT r.id,p.id FROM roles r, permissions p
WHERE r.name='admin'
ON CONFLICT DO NOTHING;
