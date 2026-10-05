-- 037: 周期人工订单（对齐魔方 CBAP 插件 CycleArtificialOrder）。
--
-- 插件可读的前端契约（template/admin/{index,cycle_order_details}.html、
-- api/cycle_order.js、js/cycle_order*.js、lang/zh-cn.js）：
--   * 生成规则：用户、订单描述、订单金额、续费金额、时间范围（开始 / 结束，
--     结束可留空）、生成周期（num + unit：day/month/year）。
--   * 到点后为该用户生成一笔人工订单；首次按订单金额，之后按续费金额；
--     详情页列出这些子订单，支持筛选 / 调整价格 / 标记支付（可勾选
--     「优先扣除余额」）/ 删除 / 批量删除（可勾选连带删除产品）。
--   * 变更生成周期后，从最近一次已生成订单的日期重新起算（cycle_tip1）。
--
-- 站内落地：
--   * orders.kind 增加 'artificial'；同时放开升级订单一直在写、但被旧 CHECK
--     拒绝的 'upgrade'。
--   * order_items.product_id 允许 NULL：人工订单没有商品，明细行只承载
--     描述 + 金额（服务端不会为其开通服务）。
--   * 子订单通过 orders.cycle_artificial_order_id 关联生成规则。

ALTER TABLE orders DROP CONSTRAINT IF EXISTS orders_kind_check;
ALTER TABLE orders ADD CONSTRAINT orders_kind_check CHECK (kind IN ('new','renewal','upgrade','artificial'));
ALTER TABLE order_items ALTER COLUMN product_id DROP NOT NULL;

CREATE TABLE IF NOT EXISTS cycle_artificial_orders (
    id BIGSERIAL PRIMARY KEY,
    public_id UUID NOT NULL DEFAULT gen_random_uuid() UNIQUE,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    description TEXT NOT NULL,
    amount_cents BIGINT NOT NULL CHECK (amount_cents >= 0),
    renew_amount_cents BIGINT NOT NULL DEFAULT 0 CHECK (renew_amount_cents >= 0),
    start_at TIMESTAMPTZ NOT NULL,
    end_at TIMESTAMPTZ,
    num INTEGER NOT NULL CHECK (num > 0),
    unit TEXT NOT NULL CHECK (unit IN ('day','month','year')),
    last_generated_at TIMESTAMPTZ,
    next_generate_at TIMESTAMPTZ,
    generated_count INTEGER NOT NULL DEFAULT 0 CHECK (generated_count >= 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_cycle_artificial_due ON cycle_artificial_orders(next_generate_at) WHERE next_generate_at IS NOT NULL;

ALTER TABLE orders ADD COLUMN IF NOT EXISTS cycle_artificial_order_id BIGINT REFERENCES cycle_artificial_orders(id) ON DELETE SET NULL;
CREATE INDEX IF NOT EXISTS idx_orders_cycle_artificial ON orders(cycle_artificial_order_id, created_at DESC);

INSERT INTO permissions(name, description) VALUES
('cycle_order.manage','管理周期人工订单（生成规则/标记支付/调整价格）')
ON CONFLICT(name) DO NOTHING;

INSERT INTO role_permissions(role_id, permission_id)
SELECT r.id,p.id FROM roles r, permissions p
WHERE r.name='admin' AND p.name='cycle_order.manage'
ON CONFLICT DO NOTHING;
