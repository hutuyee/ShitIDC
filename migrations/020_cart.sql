-- 020: 购物车多商品结算（对应魔方 shd_cart_session）。
--
-- 设计取舍：
--   * 购物车**不存价格**。每次读取都用与下单完全相同的计价逻辑重算，
--     这样组折扣、专属价、配置项加价、库存变化都会立刻反映出来，
--     也不会出现「加购时 100 元、结算时按 100 元卖但市价已 120」的问题。
--   * 一次结算生成一个 checkout_group，下面挂每个商品各自的订单。
--     订单仍然一笔一单（发票、退款、升降级都按单处理），
--     checkout_group 只负责「一起付款」。
--   * 同一个购物车只允许一种币种：多币种钱包是隔离的，一次付款只能扣一种。

CREATE TABLE IF NOT EXISTS cart_items (
    id BIGSERIAL PRIMARY KEY,
    public_id UUID NOT NULL DEFAULT gen_random_uuid() UNIQUE,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    product_id BIGINT NOT NULL REFERENCES products(id) ON DELETE CASCADE,
    billing_cycle TEXT NOT NULL,
    currency CHAR(3) NOT NULL DEFAULT 'CNY',
    quantity INTEGER NOT NULL DEFAULT 1 CHECK (quantity > 0 AND quantity <= 100),
    -- 配置项选择与自定义字段，格式与订单一致
    config_selections JSONB NOT NULL DEFAULT '[]'::jsonb,
    custom_fields JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- 同一商品同一周期同一套配置只留一行
    UNIQUE (user_id, product_id, billing_cycle, currency)
);
CREATE INDEX IF NOT EXISTS idx_cart_items_user ON cart_items(user_id);

-- 结算批次：一次「合并付款」产生的所有订单共享一个 group。
CREATE TABLE IF NOT EXISTS checkout_groups (
    id BIGSERIAL PRIMARY KEY,
    public_id UUID NOT NULL DEFAULT gen_random_uuid() UNIQUE,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    currency CHAR(3) NOT NULL,
    -- 下单时算出的总额，用于付款前的一致性校验
    total_cents BIGINT NOT NULL CHECK (total_cents >= 0),
    order_count INTEGER NOT NULL DEFAULT 0,
    status TEXT NOT NULL DEFAULT 'unpaid' CHECK (status IN ('unpaid','paid','cancelled')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    paid_at TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS idx_checkout_groups_user ON checkout_groups(user_id);

ALTER TABLE orders ADD COLUMN IF NOT EXISTS checkout_group_id BIGINT REFERENCES checkout_groups(id) ON DELETE SET NULL;
CREATE INDEX IF NOT EXISTS idx_orders_checkout_group ON orders(checkout_group_id) WHERE checkout_group_id IS NOT NULL;
