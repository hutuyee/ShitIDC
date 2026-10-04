-- Orders with full product info & cancellation, online payment providers (epay),
-- system settings (SMTP / mail), email verification codes and user behavior audit.
-- Safe for an existing ShitIDC database: every schema addition is guarded.

ALTER TABLE orders ADD COLUMN IF NOT EXISTS cancelled_at TIMESTAMPTZ;
ALTER TABLE orders ADD COLUMN IF NOT EXISTS paid_at TIMESTAMPTZ;
CREATE INDEX IF NOT EXISTS idx_orders_unpaid_created ON orders(created_at) WHERE status = 'unpaid';

ALTER TABLE users ADD COLUMN IF NOT EXISTS email_verified BOOLEAN NOT NULL DEFAULT FALSE;
-- Existing accounts were created by the operator before verification existed; grandfather them in.
UPDATE users SET email_verified = TRUE WHERE email_verified = FALSE;

CREATE TABLE IF NOT EXISTS system_settings (
    key TEXT PRIMARY KEY,
    value JSONB NOT NULL DEFAULT '{}'::jsonb,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS email_verification_codes (
    email TEXT NOT NULL,
    purpose TEXT NOT NULL,
    code_hash TEXT NOT NULL,
    attempts INTEGER NOT NULL DEFAULT 0,
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (email, purpose)
);
CREATE INDEX IF NOT EXISTS idx_email_codes_created ON email_verification_codes(created_at);

CREATE TABLE IF NOT EXISTS payment_providers (
    id BIGSERIAL PRIMARY KEY,
    public_id UUID NOT NULL DEFAULT gen_random_uuid() UNIQUE,
    name TEXT NOT NULL,
    method TEXT NOT NULL,
    gateway_url TEXT NOT NULL DEFAULT '',
    merchant_id TEXT NOT NULL DEFAULT '',
    secret_encrypted TEXT NOT NULL DEFAULT '',
    config JSONB NOT NULL DEFAULT '{}'::jsonb,
    active BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_audit_action ON audit_logs(action);
CREATE INDEX IF NOT EXISTS idx_audit_created ON audit_logs(created_at DESC);
