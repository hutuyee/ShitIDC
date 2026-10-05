-- 036: 活动促销（对齐魔方 CBAP 插件 EventPromotion）。
--
-- 插件可读的前端契约（template/admin/api/index.js、event_detail.js、index.js、
-- lang/zh-cn.js）给出的字段面：活动名称、生效 / 截止时间（Unix 秒）、类型
-- percent（折扣比例 %）/ reduce（满减：达标金额 full、优惠金额 value）、
-- 参与产品 products[]、适用用户 clients[]（不选 = 所有用户，client_type
-- all/appoint）、开关 new_user（仅新注册用户）/ old_user（仅现有客户，需有
-- 已核验订单）/ single_user_once（单用户一次）/ cycle_limit + cycle[]（周期限制）、
-- 备注、启停、排序（置顶置底），列表状态 Active / Pending / Expiration / Suspended。
--
-- 金额一律落库为「分」；比例落库为 percent_bp INTEGER（百分比的 100 倍，
-- 即基点：950 = 9.5%，10000 = 100%），避免浮点与 NUMERIC 扫描。

CREATE TABLE IF NOT EXISTS promotions (
    id BIGSERIAL PRIMARY KEY,
    public_id UUID NOT NULL DEFAULT gen_random_uuid() UNIQUE,
    name TEXT NOT NULL,
    type TEXT NOT NULL CHECK (type IN ('percent','reduce')),
    -- percent：折扣比例的基点（9.5% => 950）。
    percent_bp INTEGER NOT NULL DEFAULT 0 CHECK (percent_bp >= 0 AND percent_bp <= 10000),
    -- reduce：达标金额（满）与优惠金额（减），单位分。
    full_cents BIGINT NOT NULL DEFAULT 0 CHECK (full_cents >= 0),
    reduce_cents BIGINT NOT NULL DEFAULT 0 CHECK (reduce_cents >= 0),
    start_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    end_at TIMESTAMPTZ,
    -- 参与活动的商品（空 = 全部商品）。
    product_ids BIGINT[] NOT NULL DEFAULT '{}',
    -- 适用用户：all 所有用户 / appoint 指定用户（client_ids）。
    client_type TEXT NOT NULL DEFAULT 'all' CHECK (client_type IN ('all','appoint')),
    client_ids BIGINT[] NOT NULL DEFAULT '{}',
    -- new_user：仅限没有已支付订单的用户；old_user：仅限已有已支付订单的用户。
    -- 两个开关同时开启时视为「两类用户都可参与」。
    new_user BOOLEAN NOT NULL DEFAULT FALSE,
    old_user BOOLEAN NOT NULL DEFAULT FALSE,
    single_user_once BOOLEAN NOT NULL DEFAULT FALSE,
    cycle_limit BOOLEAN NOT NULL DEFAULT FALSE,
    cycle TEXT[] NOT NULL DEFAULT '{}',
    notes TEXT NOT NULL DEFAULT '',
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    sort_order INT NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_promotions_active ON promotions(enabled, sort_order, id);

-- 订单上记录命中的活动：单用户一次与结算展示都靠它。
ALTER TABLE orders ADD COLUMN IF NOT EXISTS promotion_id BIGINT REFERENCES promotions(id) ON DELETE SET NULL;
CREATE INDEX IF NOT EXISTS idx_orders_promotion ON orders(promotion_id, user_id);

INSERT INTO permissions(name, description) VALUES
('promotion.manage','管理活动促销（创建/启停/排序/指定商品与用户）')
ON CONFLICT(name) DO NOTHING;

INSERT INTO role_permissions(role_id, permission_id)
SELECT r.id,p.id FROM roles r, permissions p
WHERE r.name='admin' AND p.name='promotion.manage'
ON CONFLICT DO NOTHING;
