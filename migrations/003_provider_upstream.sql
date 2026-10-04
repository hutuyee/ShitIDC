-- Persistent upstream provider management and product mapping.
-- Safe for an existing ShitIDC database: every schema addition is guarded.

ALTER TABLE providers ADD COLUMN IF NOT EXISTS status TEXT NOT NULL DEFAULT 'unknown';
ALTER TABLE providers ADD COLUMN IF NOT EXISTS last_checked_at TIMESTAMPTZ;
ALTER TABLE providers ADD COLUMN IF NOT EXISTS last_sync_at TIMESTAMPTZ;
ALTER TABLE providers ADD COLUMN IF NOT EXISTS last_error TEXT NOT NULL DEFAULT '';

ALTER TABLE products ADD COLUMN IF NOT EXISTS provider_id BIGINT REFERENCES providers(id) ON DELETE RESTRICT;
CREATE INDEX IF NOT EXISTS idx_products_provider ON products(provider_id) WHERE provider_id IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS ux_products_provider_ref ON products(provider_id, provider_product_ref) WHERE provider_id IS NOT NULL AND provider_product_ref IS NOT NULL AND deleted_at IS NULL;

ALTER TABLE order_items ADD COLUMN IF NOT EXISTS provider_id BIGINT REFERENCES providers(id) ON DELETE RESTRICT;
ALTER TABLE services ADD COLUMN IF NOT EXISTS provider_id BIGINT REFERENCES providers(id) ON DELETE RESTRICT;
ALTER TABLE services ADD COLUMN IF NOT EXISTS provider_product_ref TEXT;
CREATE INDEX IF NOT EXISTS idx_services_provider ON services(provider_id) WHERE provider_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS provider_products (
    id BIGSERIAL PRIMARY KEY,
    provider_id BIGINT NOT NULL REFERENCES providers(id) ON DELETE CASCADE,
    upstream_product_id TEXT NOT NULL,
    name TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    price_cents BIGINT NOT NULL DEFAULT 0 CHECK (price_cents >= 0),
    currency CHAR(3) NOT NULL DEFAULT 'CNY',
    billing_cycle TEXT NOT NULL DEFAULT 'monthly',
    raw_payload JSONB NOT NULL DEFAULT '{}'::jsonb,
    synced_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE(provider_id, upstream_product_id)
);
CREATE INDEX IF NOT EXISTS idx_provider_products_provider ON provider_products(provider_id, name);
