-- Service lifecycle tracking, outbound webhooks, login security logs,
-- product grouping and the new RBAC permissions they rely on.
-- Safe for an existing ShitIDC database: every schema addition is guarded.

-- 1. Service lifecycle timestamps + expiry index for the auto-suspend job.
ALTER TABLE services ADD COLUMN IF NOT EXISTS suspended_at TIMESTAMPTZ;
ALTER TABLE services ADD COLUMN IF NOT EXISTS terminated_at TIMESTAMPTZ;
CREATE INDEX IF NOT EXISTS idx_services_expires_active ON services(expires_at) WHERE status = 'active';
CREATE INDEX IF NOT EXISTS idx_services_status_updated ON services(status, updated_at);

-- Full state machine from the roadmap (§74): add the missing 'unsuspending'
-- transitional state to the status check.
ALTER TABLE services DROP CONSTRAINT IF EXISTS services_status_check;
DO $$ BEGIN
    ALTER TABLE services ADD CONSTRAINT services_status_check
    CHECK (status IN ('pending','provisioning','active','suspending','unsuspending','suspended','terminating','terminated','failed'));
EXCEPTION WHEN duplicate_object THEN NULL; END $$;

-- 2. Product groups (产品分组).
CREATE TABLE IF NOT EXISTS product_groups (
    id BIGSERIAL PRIMARY KEY,
    public_id UUID NOT NULL DEFAULT gen_random_uuid() UNIQUE,
    name TEXT NOT NULL UNIQUE,
    sort_weight INTEGER NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
ALTER TABLE products ADD COLUMN IF NOT EXISTS group_id BIGINT REFERENCES product_groups(id) ON DELETE SET NULL;
ALTER TABLE products ADD COLUMN IF NOT EXISTS sort_weight INTEGER NOT NULL DEFAULT 0;

-- 3. Outbound webhooks: HMAC-signed delivery of core events to third parties.
CREATE TABLE IF NOT EXISTS webhooks (
    id BIGSERIAL PRIMARY KEY,
    public_id UUID NOT NULL DEFAULT gen_random_uuid() UNIQUE,
    name TEXT NOT NULL,
    url TEXT NOT NULL,
    secret_encrypted TEXT NOT NULL,
    events TEXT[] NOT NULL DEFAULT '{}',
    active BOOLEAN NOT NULL DEFAULT TRUE,
    last_delivery_at TIMESTAMPTZ,
    last_delivery_ok BOOLEAN,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS webhook_deliveries (
    id BIGSERIAL PRIMARY KEY,
    public_id UUID NOT NULL DEFAULT gen_random_uuid() UNIQUE,
    webhook_id BIGINT NOT NULL REFERENCES webhooks(id) ON DELETE CASCADE,
    event TEXT NOT NULL,
    payload JSONB NOT NULL DEFAULT '{}'::jsonb,
    status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','delivered','failed')),
    attempts INTEGER NOT NULL DEFAULT 0,
    response_code INTEGER,
    last_error TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    delivered_at TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS idx_webhook_deliveries_webhook ON webhook_deliveries(webhook_id, created_at DESC);

-- 4. Login security log (per-attempt, success and failure).
CREATE TABLE IF NOT EXISTS user_login_logs (
    id BIGSERIAL PRIMARY KEY,
    user_id BIGINT REFERENCES users(id) ON DELETE SET NULL,
    email TEXT NOT NULL DEFAULT '',
    success BOOLEAN NOT NULL,
    reason TEXT NOT NULL DEFAULT '',
    ip INET,
    user_agent TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_login_logs_user_created ON user_login_logs(user_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_login_logs_email_created ON user_login_logs(email, created_at DESC);

-- 5. New permissions: service lifecycle console, webhook management,
--    user administration, full PII view.
INSERT INTO permissions(name, description) VALUES
('service.manage','查看所有用户的服务并执行暂停/解除/终止（管理员）'),
('webhook.manage','管理出站 Webhook'),
('user.write','禁用/启用用户、重置安全状态'),
('pii.read.full','查看用户完整隐私信息（手机号等），操作会被审计')
ON CONFLICT(name) DO NOTHING;

INSERT INTO role_permissions(role_id, permission_id)
SELECT r.id,p.id FROM roles r, permissions p
WHERE r.name='admin'
ON CONFLICT DO NOTHING;
