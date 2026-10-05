-- 035: 代金券（对齐魔方 CBAP 插件 IdcsmartVoucher）。
--
-- 与站内「优惠券」（码核销式折扣码）不同：代金券是**发放 / 领取式**的定额抵扣券，
-- 后台生成券码，公开券可被用户在前台领取，私有券只能由后台按用户发放；
-- 下单 / 续费 / 升降级时按订单金额核销（金额不超过应付金额），并留下使用记录。
--
-- 券码规则与插件一致：8 位、同时包含大写字母、小写字母与数字。

CREATE TABLE IF NOT EXISTS vouchers (
    id BIGSERIAL PRIMARY KEY,
    public_id UUID NOT NULL DEFAULT gen_random_uuid() UNIQUE,
    code TEXT NOT NULL UNIQUE,
    price_cents BIGINT NOT NULL CHECK (price_cents >= 0),
    -- private: 仅后台发放；public: 前台可领取（并受 num 总量限制）。
    type TEXT NOT NULL DEFAULT 'private' CHECK (type IN ('private','public')),
    -- 可发放总量（0 = 无限制）。
    num BIGINT NOT NULL DEFAULT 0 CHECK (num >= 0),
    start_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    end_at TIMESTAMPTZ,
    -- 订单必须包含的产品（空 = 无要求）。
    product_ids BIGINT[] NOT NULL DEFAULT '{}',
    -- 用户必须拥有激活产品的产品（空 = 无要求）。
    product_need_ids BIGINT[] NOT NULL DEFAULT '{}',
    min_amount_cents BIGINT NOT NULL DEFAULT 0 CHECK (min_amount_cents >= 0),
    -- no_limit 无限制 / no_host 仅限无产品用户 / need_active 需存在激活中产品。
    user_type TEXT NOT NULL DEFAULT 'no_limit' CHECK (user_type IN ('no_limit','no_host','need_active')),
    onetime BOOLEAN NOT NULL DEFAULT FALSE,
    upgrade_use BOOLEAN NOT NULL DEFAULT FALSE,
    renew_use BOOLEAN NOT NULL DEFAULT FALSE,
    -- 适用计费周期（空 = 不限）。
    cycle TEXT[] NOT NULL DEFAULT '{}',
    notes TEXT NOT NULL DEFAULT '',
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_vouchers_code ON vouchers(code);

CREATE TABLE IF NOT EXISTS voucher_grants (
    id BIGSERIAL PRIMARY KEY,
    public_id UUID NOT NULL DEFAULT gen_random_uuid() UNIQUE,
    voucher_id BIGINT NOT NULL REFERENCES vouchers(id) ON DELETE CASCADE,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    source TEXT NOT NULL DEFAULT 'grant' CHECK (source IN ('grant','claim')),
    used BOOLEAN NOT NULL DEFAULT FALSE,
    order_id BIGINT REFERENCES orders(id) ON DELETE SET NULL,
    used_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_voucher_grants_voucher ON voucher_grants(voucher_id, id);
CREATE INDEX IF NOT EXISTS idx_voucher_grants_user ON voucher_grants(user_id, used);

INSERT INTO permissions(name, description) VALUES
('voucher.manage','管理代金券（创建/发放/停用/使用记录）')
ON CONFLICT(name) DO NOTHING;

INSERT INTO role_permissions(role_id, permission_id)
SELECT r.id,p.id FROM roles r, permissions p
WHERE r.name='admin' AND p.name='voucher.manage'
ON CONFLICT DO NOTHING;
