-- 031: 成本支出（对齐魔方 CBAP 插件 cost_pay「成本支出」）。
--
-- 后台在订单详情内登记支出记录（名称/主体/金额/支出时间/备注），
-- 并支持「自定义字段」管理：字段类型文本框/下拉、是否必填、列表展示、排序。
-- 金额口径：插件用「元」小数展示，本站按仓库惯例存「分」整数。

CREATE TABLE IF NOT EXISTS cost_pay_fields (
    id BIGSERIAL PRIMARY KEY,
    public_id UUID NOT NULL DEFAULT gen_random_uuid() UNIQUE,
    field_name TEXT NOT NULL,
    -- text 文本框 / dropdown 下拉
    field_type TEXT NOT NULL DEFAULT 'text' CHECK (field_type IN ('text','dropdown')),
    is_required BOOLEAN NOT NULL DEFAULT FALSE,
    -- 下拉值，英文半角逗号分隔
    field_option TEXT NOT NULL DEFAULT '',
    -- 是否在支出列表展示为列
    show_list BOOLEAN NOT NULL DEFAULT TRUE,
    sort_weight BIGINT NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS order_cost_pays (
    id BIGSERIAL PRIMARY KEY,
    public_id UUID NOT NULL DEFAULT gen_random_uuid() UNIQUE,
    order_id BIGINT NOT NULL REFERENCES orders(id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    owner TEXT NOT NULL,
    cost_cents BIGINT NOT NULL DEFAULT 0 CHECK (cost_cents >= 0),
    cost_time TIMESTAMPTZ NOT NULL,
    notes TEXT NOT NULL DEFAULT '',
    admin_id BIGINT REFERENCES users(id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_order_cost_pays_order ON order_cost_pays(order_id, cost_time DESC);

CREATE TABLE IF NOT EXISTS order_cost_pay_values (
    id BIGSERIAL PRIMARY KEY,
    cost_pay_id BIGINT NOT NULL REFERENCES order_cost_pays(id) ON DELETE CASCADE,
    field_id BIGINT NOT NULL REFERENCES cost_pay_fields(id) ON DELETE CASCADE,
    value TEXT NOT NULL DEFAULT '',
    UNIQUE (cost_pay_id, field_id)
);
