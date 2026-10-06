-- 业务经理（对齐魔方 CBAP IdcsmartSale 插件）。
-- 销售成员（挂管理员账号）、客户绑定、提成规则（全局 / 商品级：新购 / 续费 /
-- 复购 / 升降级，fixed 或 percent）、大额订单奖励、支付成功按规则记提成
-- （确认天数后生效），以及销售统计 / 排名 / 提成详情。

CREATE TABLE IF NOT EXISTS sales (
    id BIGSERIAL PRIMARY KEY,
    public_id UUID NOT NULL DEFAULT gen_random_uuid() UNIQUE,
    admin_id BIGINT NOT NULL UNIQUE REFERENCES users(id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    num TEXT NOT NULL DEFAULT '',
    email TEXT NOT NULL DEFAULT '',
    active BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS sale_clients (
    sale_id BIGINT NOT NULL REFERENCES sales(id) ON DELETE CASCADE,
    user_id BIGINT NOT NULL UNIQUE REFERENCES users(id) ON DELETE CASCADE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_sale_clients_sale ON sale_clients(sale_id);

CREATE TABLE IF NOT EXISTS sale_commission_configs (
    id BIGSERIAL PRIMARY KEY,
    public_id UUID NOT NULL DEFAULT gen_random_uuid() UNIQUE,
    scope TEXT NOT NULL CHECK (scope IN ('global','product')),
    product_id BIGINT REFERENCES products(id) ON DELETE CASCADE,
    new_mode TEXT NOT NULL DEFAULT 'fixed' CHECK (new_mode IN ('fixed','percent')),
    new_value BIGINT NOT NULL DEFAULT 0 CHECK (new_value >= 0),
    renew_mode TEXT NOT NULL DEFAULT 'fixed' CHECK (renew_mode IN ('fixed','percent')),
    renew_value BIGINT NOT NULL DEFAULT 0 CHECK (renew_value >= 0),
    repurchase_mode TEXT NOT NULL DEFAULT 'fixed' CHECK (repurchase_mode IN ('fixed','percent')),
    repurchase_value BIGINT NOT NULL DEFAULT 0 CHECK (repurchase_value >= 0),
    upgrade_mode TEXT NOT NULL DEFAULT 'fixed' CHECK (upgrade_mode IN ('fixed','percent')),
    upgrade_value BIGINT NOT NULL DEFAULT 0 CHECK (upgrade_value >= 0),
    active BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT sale_config_scope_unique UNIQUE (scope, product_id)
);

CREATE TABLE IF NOT EXISTS sale_commissions (
    id BIGSERIAL PRIMARY KEY,
    public_id UUID NOT NULL DEFAULT gen_random_uuid() UNIQUE,
    idem_key TEXT NOT NULL UNIQUE,
    sale_id BIGINT NOT NULL REFERENCES sales(id) ON DELETE CASCADE,
    order_id BIGINT REFERENCES orders(id) ON DELETE SET NULL,
    user_id BIGINT REFERENCES users(id) ON DELETE SET NULL,
    type TEXT NOT NULL CHECK (type IN ('new','renew','repurchase','upgrade','big_order')),
    base_cents BIGINT NOT NULL DEFAULT 0,
    mode TEXT NOT NULL DEFAULT 'fixed' CHECK (mode IN ('fixed','percent')),
    value BIGINT NOT NULL DEFAULT 0,
    amount_cents BIGINT NOT NULL DEFAULT 0,
    status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','active','invalid')),
    confirm_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_sale_commissions_sale ON sale_commissions(sale_id, id DESC);
CREATE INDEX IF NOT EXISTS idx_sale_commissions_status ON sale_commissions(status);

INSERT INTO permissions(name, description) VALUES
('sale.manage','管理业务经理（销售成员 / 用户绑定 / 提成规则 / 统计）')
ON CONFLICT(name) DO NOTHING;

INSERT INTO role_permissions(role_id, permission_id)
SELECT r.id,p.id FROM roles r, permissions p
WHERE r.name='admin' AND p.name='sale.manage'
ON CONFLICT DO NOTHING;
