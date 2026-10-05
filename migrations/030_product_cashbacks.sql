-- 030: 商品返现（对齐魔方 CBAP 插件 product_cashback「商品返现」）。
--
-- 按商品配置返现规则；订单支付成功后把返现金额计入买家余额（同订单只返一次）。
-- 「可返现期限」按契约保存：0=永久，N=购买后 N 天内（超期订单不返现）。

CREATE TABLE IF NOT EXISTS product_cashbacks (
    id BIGSERIAL PRIMARY KEY,
    public_id UUID NOT NULL DEFAULT gen_random_uuid() UNIQUE,
    product_id BIGINT NOT NULL REFERENCES products(id) ON DELETE CASCADE,
    -- 返现类型：fixed 固定金额（当前插件只提供这一种）
    type TEXT NOT NULL DEFAULT 'fixed',
    -- 返现金额（分）
    price_cents BIGINT NOT NULL DEFAULT 0 CHECK (price_cents >= 0),
    -- 可返现期限（天）：0=永久
    period_days INTEGER NOT NULL DEFAULT 0 CHECK (period_days >= 0),
    active BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (product_id)
);
