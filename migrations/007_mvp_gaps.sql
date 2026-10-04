-- MVP gap fill: API request logs (roadmap §5.6), announcements, TOTP 2FA
-- credentials and the announcement management permission.
-- Safe for an existing ShitIDC database: every schema addition is guarded.

-- 1. api_logs: one row per API request (第九/十二阶段配套，§5.6 审计三表之一).
--    Query strings are never stored — only the path — so signed callbacks or
--    token-bearing URLs cannot leak through logs.
CREATE TABLE IF NOT EXISTS api_logs (
    id BIGSERIAL PRIMARY KEY,
    method TEXT NOT NULL,
    path TEXT NOT NULL,
    status INTEGER NOT NULL,
    user_id BIGINT REFERENCES users(id) ON DELETE SET NULL,
    api_token BOOLEAN NOT NULL DEFAULT FALSE,
    error_code TEXT NOT NULL DEFAULT '',
    request_id TEXT NOT NULL DEFAULT '',
    ip INET,
    user_agent TEXT NOT NULL DEFAULT '',
    duration_ms INTEGER NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_api_logs_created ON api_logs(created_at DESC);
CREATE INDEX IF NOT EXISTS idx_api_logs_user_created ON api_logs(user_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_api_logs_status ON api_logs(status) WHERE status >= 400;

-- 2. Announcements: replaces the hardcoded dashboard notice with real data.
CREATE TABLE IF NOT EXISTS announcements (
    id BIGSERIAL PRIMARY KEY,
    public_id UUID NOT NULL DEFAULT gen_random_uuid() UNIQUE,
    title TEXT NOT NULL,
    body TEXT NOT NULL DEFAULT '',
    active BOOLEAN NOT NULL DEFAULT TRUE,
    pinned BOOLEAN NOT NULL DEFAULT FALSE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_announcements_active ON announcements(created_at DESC) WHERE active = TRUE;

-- 3. TOTP two-factor credentials (§9 可选 2FA). The secret is stored
--    AES-256-GCM encrypted with the master key, never in plaintext.
ALTER TABLE user_security ADD COLUMN IF NOT EXISTS totp_secret_encrypted TEXT;
ALTER TABLE user_security ADD COLUMN IF NOT EXISTS totp_enabled_at TIMESTAMPTZ;

-- 4. Announcement management permission for the admin console.
INSERT INTO permissions(name, description) VALUES
('announcement.manage','发布公告与管理站内公告')
ON CONFLICT(name) DO NOTHING;

INSERT INTO role_permissions(role_id, permission_id)
SELECT r.id,p.id FROM roles r, permissions p
WHERE r.name='admin' AND p.name='announcement.manage'
ON CONFLICT DO NOTHING;
