-- 017: 客户组按产品差异定价（对应魔方 shd_user_product_bates）。
--
-- 现状：user_groups.discount_percent 是一个全局折扣，对组内所有商品一视同仁。
-- 魔方还支持「某个组 + 某个商品 + 某个周期」单独定价，比如：
--   VIP 组买「香港小鸡」月付 8 折，但买「美国杜甫」月付只要 50 元（不是打折而是定死价）。
--
-- 价格优先级（高 -> 低）：
--   1. user_product_prices 里「该用户所属组 + 该商品 + 该周期」的固定价
--   2. products.discount_percent_override？没有——用组折扣
--   3. product_prices 的标价
--
-- 组折扣（discount_percent）在固定价之上不再叠加：固定价就是固定价，
-- 否则运营很难解释「为什么标 50 元最后收了 45 元」。

CREATE TABLE IF NOT EXISTS user_product_prices (
    id BIGSERIAL PRIMARY KEY,
    group_id BIGINT NOT NULL REFERENCES user_groups(id) ON DELETE CASCADE,
    product_id BIGINT NOT NULL REFERENCES products(id) ON DELETE CASCADE,
    billing_cycle TEXT NOT NULL,
    amount_cents BIGINT NOT NULL CHECK (amount_cents >= 0),
    -- 可选：只对某些周期生效；NULL 表示该周期的价格就是 amount_cents
    currency CHAR(3) NOT NULL DEFAULT 'CNY',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (group_id, product_id, billing_cycle, currency)
);
CREATE INDEX IF NOT EXISTS idx_user_product_prices_group ON user_product_prices(group_id);

-- 让定价可以按币种独立（与 product_prices 一致）。
ALTER TABLE user_product_prices ADD COLUMN IF NOT EXISTS currency CHAR(3) NOT NULL DEFAULT 'CNY';
DO $$ BEGIN
    ALTER TABLE user_product_prices ADD CONSTRAINT user_product_prices_cycle_check
    CHECK (billing_cycle IN ('hourly','daily','monthly','quarterly','semiannually','yearly',
        'biennially','triennially','fourly','fively','sixly','sevenly','eightly','ninely','tenly','onetime'));
EXCEPTION WHEN duplicate_object THEN NULL; END $$;
