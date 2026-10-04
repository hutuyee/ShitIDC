-- 012: 升降级（对应魔方 shd_upgrades + server 模块回调 _ChangePackage）。
--
-- 换算方式与 WHMCS / 魔方一致：按剩余天数把当前服务未使用的价值折算成金额，
-- 再与新方案的周期价比差价。差价为 0 或负数（降级）时立即生效且不退款；
-- 差价为正在生成一张升级订单，付款后才真正切换。

CREATE TABLE IF NOT EXISTS service_upgrades (
    id BIGSERIAL PRIMARY KEY,
    public_id UUID NOT NULL DEFAULT gen_random_uuid() UNIQUE,
    service_id BIGINT NOT NULL REFERENCES services(id) ON DELETE CASCADE,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    from_product_id BIGINT REFERENCES products(id) ON DELETE SET NULL,
    to_product_id BIGINT NOT NULL REFERENCES products(id) ON DELETE RESTRICT,
    from_cycle TEXT NOT NULL DEFAULT '',
    to_cycle TEXT NOT NULL DEFAULT '',
    -- 剩余价值折算：remaining_value_cents = 旧周期价 × 剩余天数 / 周期总天数
    days_remaining INTEGER NOT NULL DEFAULT 0,
    days_in_cycle INTEGER NOT NULL DEFAULT 0,
    remaining_value_cents BIGINT NOT NULL DEFAULT 0,
    -- 新方案价格与差价
    new_price_cents BIGINT NOT NULL DEFAULT 0,
    diff_cents BIGINT NOT NULL DEFAULT 0,        -- >0 需补款，<=0 立即生效
    order_id BIGINT REFERENCES orders(id) ON DELETE SET NULL,
    status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','paid','applied','cancelled','failed')),
    applied_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_service_upgrades_service ON service_upgrades(service_id);
CREATE INDEX IF NOT EXISTS idx_service_upgrades_status ON service_upgrades(status);

-- 订单侧关联：付款后据此应用升级
ALTER TABLE orders ADD COLUMN IF NOT EXISTS upgrade_id BIGINT REFERENCES service_upgrades(id) ON DELETE SET NULL;
CREATE INDEX IF NOT EXISTS idx_orders_upgrade ON orders(upgrade_id) WHERE upgrade_id IS NOT NULL;

-- 服务的配置项选择（升级时可一并变更配置项，魔方的 configoptions_upgrade）
ALTER TABLE services ADD COLUMN IF NOT EXISTS config_selections JSONB NOT NULL DEFAULT '[]'::jsonb;
