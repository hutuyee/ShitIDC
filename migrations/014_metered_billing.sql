-- 014: 按量 / 超量计费（对应魔方 shd_products 的 overages_* 与 shd_dcim_* 流量包体系）。
--
-- 模型（与 WHMCS / 魔方一致）：每个计量资源有一份"包含额度"，用超了就按单价计费。
--   included_*  包含额度（0 = 不含，全部按量）
--   limit_*     硬上限，超过则暂停服务（0 = 不限制）
--   price_*_cents 每单位超出部分的价格
--   usage_*     当前周期内的用量
-- 用量由 Provider 回报（Sync/Status 时写入 provider_payload），出账时按此表计费。

-- 1. 商品侧的超量配置
ALTER TABLE products ADD COLUMN IF NOT EXISTS overage_enabled BOOLEAN NOT NULL DEFAULT FALSE;
-- 磁盘：单位 MB
ALTER TABLE products ADD COLUMN IF NOT EXISTS disk_included_mb BIGINT NOT NULL DEFAULT 0 CHECK (disk_included_mb >= 0);
ALTER TABLE products ADD COLUMN IF NOT EXISTS disk_limit_mb BIGINT NOT NULL DEFAULT 0 CHECK (disk_limit_mb >= 0);
ALTER TABLE products ADD COLUMN IF NOT EXISTS disk_price_cents_per_gb BIGINT NOT NULL DEFAULT 0 CHECK (disk_price_cents_per_gb >= 0);
-- 流量：单位 GB
ALTER TABLE products ADD COLUMN IF NOT EXISTS bw_included_gb BIGINT NOT NULL DEFAULT 0 CHECK (bw_included_gb >= 0);
ALTER TABLE products ADD COLUMN IF NOT EXISTS bw_limit_gb BIGINT NOT NULL DEFAULT 0 CHECK (bw_limit_gb >= 0);
ALTER TABLE products ADD COLUMN IF NOT EXISTS bw_price_cents_per_gb BIGINT NOT NULL DEFAULT 0 CHECK (bw_price_cents_per_gb >= 0);
-- 计费方式：cycle_end 周期末出账 / immediate 立即扣费
ALTER TABLE products ADD COLUMN IF NOT EXISTS overage_billing TEXT NOT NULL DEFAULT 'cycle_end';
DO $$ BEGIN
    ALTER TABLE products ADD CONSTRAINT products_overage_billing_check
    CHECK (overage_billing IN ('cycle_end','immediate'));
EXCEPTION WHEN duplicate_object THEN NULL; END $$;

-- 2. 服务实例侧的用量与周期末计费状态
ALTER TABLE services ADD COLUMN IF NOT EXISTS usage_disk_mb BIGINT NOT NULL DEFAULT 0 CHECK (usage_disk_mb >= 0);
ALTER TABLE services ADD COLUMN IF NOT EXISTS usage_bw_gb BIGINT NOT NULL DEFAULT 0 CHECK (usage_bw_gb >= 0);
ALTER TABLE services ADD COLUMN IF NOT EXISTS usage_reported_at TIMESTAMPTZ;
-- 已出账到的用量水位：只对"水位之上"的部分收费，避免重复计费
ALTER TABLE services ADD COLUMN IF NOT EXISTS billed_disk_mb BIGINT NOT NULL DEFAULT 0 CHECK (billed_disk_mb >= 0);
ALTER TABLE services ADD COLUMN IF NOT EXISTS billed_bw_gb BIGINT NOT NULL DEFAULT 0 CHECK (billed_bw_gb >= 0);
ALTER TABLE services ADD COLUMN IF NOT EXISTS overage_suspended_at TIMESTAMPTZ;

-- 3. 超量账单：单独记录每一笔，便于对账与申诉
CREATE TABLE IF NOT EXISTS overage_charges (
    id BIGSERIAL PRIMARY KEY,
    public_id UUID NOT NULL DEFAULT gen_random_uuid() UNIQUE,
    service_id BIGINT NOT NULL REFERENCES services(id) ON DELETE CASCADE,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    order_id BIGINT REFERENCES orders(id) ON DELETE SET NULL,
    resource TEXT NOT NULL CHECK (resource IN ('disk','bandwidth')),
    quantity BIGINT NOT NULL,               -- 计费数量（磁盘 MB / 流量 GB）
    unit_price_cents BIGINT NOT NULL,
    amount_cents BIGINT NOT NULL CHECK (amount_cents >= 0),
    currency CHAR(3) NOT NULL DEFAULT 'CNY',
    period_start TIMESTAMPTZ,
    period_end TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_overage_charges_service ON overage_charges(service_id);
CREATE INDEX IF NOT EXISTS idx_overage_charges_user ON overage_charges(user_id);
