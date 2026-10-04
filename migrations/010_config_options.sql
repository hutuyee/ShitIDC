-- 010: 商品配置项（configurable options）、自定义字段、库存与限购。
--
-- 对应魔方财务的三组表：
--   shd_product_config_groups / shd_product_config_links / shd_product_config_options(+_sub)
--   shd_customfields / shd_customfieldsvalues
--   shd_products.stock_control / qty / allow_qty / maximum_customer_purchase_quantity
-- 这里按 ShitIDC 的“公开 UUID + 内部自增 ID”风格重建，并把价格直接挂在子项上，
-- 不再走魔方那张多态 shd_pricing 表——ShitIDC 的价格模型本来就是一张
-- product_prices，配置项用独立的 config_option_prices 更直观。

-- ---------------------------------------------------------------------------
-- 1. 配置项分组：一组配置项可以挂到多个商品上（魔方的 config_groups + config_links）
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS config_groups (
    id BIGSERIAL PRIMARY KEY,
    public_id UUID NOT NULL DEFAULT gen_random_uuid() UNIQUE,
    name TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    sort_weight INTEGER NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS config_group_links (
    id BIGSERIAL PRIMARY KEY,
    group_id BIGINT NOT NULL REFERENCES config_groups(id) ON DELETE CASCADE,
    product_id BIGINT NOT NULL REFERENCES products(id) ON DELETE CASCADE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (group_id, product_id)
);
CREATE INDEX IF NOT EXISTS idx_config_group_links_product ON config_group_links(product_id);

-- ---------------------------------------------------------------------------
-- 2. 配置项：option_type 与魔方一致
--    1 = 下拉  dropdown
--    2 = 单选  radio
--    3 = 开关  yesno
--    4 = 数量  quantity（数量乘以子项单价）
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS config_options (
    id BIGSERIAL PRIMARY KEY,
    public_id UUID NOT NULL DEFAULT gen_random_uuid() UNIQUE,
    group_id BIGINT REFERENCES config_groups(id) ON DELETE CASCADE,
    product_id BIGINT REFERENCES products(id) ON DELETE CASCADE,   -- 直接挂商品时用
    name TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    option_type SMALLINT NOT NULL DEFAULT 1 CHECK (option_type IN (1,2,3,4)),
    required BOOLEAN NOT NULL DEFAULT TRUE,
    sort_weight INTEGER NOT NULL DEFAULT 0,
    qty_min INTEGER NOT NULL DEFAULT 1,                            -- 数量型：下限
    qty_max INTEGER NOT NULL DEFAULT 100,                          -- 数量型：上限
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- 必须挂在分组或商品其中之一，不能都不挂
    CHECK (group_id IS NOT NULL OR product_id IS NOT NULL)
);
CREATE INDEX IF NOT EXISTS idx_config_options_group ON config_options(group_id);
CREATE INDEX IF NOT EXISTS idx_config_options_product ON config_options(product_id);

-- 子项：下拉/单选的候选项、开关的“开”、数量型的计价单位
CREATE TABLE IF NOT EXISTS config_option_values (
    id BIGSERIAL PRIMARY KEY,
    public_id UUID NOT NULL DEFAULT gen_random_uuid() UNIQUE,
    option_id BIGINT NOT NULL REFERENCES config_options(id) ON DELETE CASCADE,
    label TEXT NOT NULL,
    price_cents BIGINT NOT NULL DEFAULT 0 CHECK (price_cents >= 0), -- 相对商品基础价的加价
    setup_cents BIGINT NOT NULL DEFAULT 0 CHECK (setup_cents >= 0), -- 一次性初装费
    is_default BOOLEAN NOT NULL DEFAULT FALSE,
    hidden BOOLEAN NOT NULL DEFAULT FALSE,
    sort_weight INTEGER NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_config_option_values_option ON config_option_values(option_id);

-- ---------------------------------------------------------------------------
-- 3. 自定义字段：下单时收集的自由文本/下拉，进订单明细并随开通请求下发
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS product_custom_fields (
    id BIGSERIAL PRIMARY KEY,
    public_id UUID NOT NULL DEFAULT gen_random_uuid() UNIQUE,
    product_id BIGINT NOT NULL REFERENCES products(id) ON DELETE CASCADE,
    name TEXT NOT NULL,                                   -- 显示名
    field_key TEXT NOT NULL,                              -- 传给 Provider 的键名
    field_type TEXT NOT NULL DEFAULT 'text' CHECK (field_type IN ('text','textarea','dropdown','password')),
    options TEXT[] NOT NULL DEFAULT '{}',                 -- dropdown 的候选项
    description TEXT NOT NULL DEFAULT '',
    placeholder TEXT NOT NULL DEFAULT '',
    required BOOLEAN NOT NULL DEFAULT FALSE,
    admin_only BOOLEAN NOT NULL DEFAULT FALSE,            -- 仅管理员可见/可填
    show_on_order BOOLEAN NOT NULL DEFAULT TRUE,          -- 下单页显示
    regex TEXT NOT NULL DEFAULT '',                       -- 校验正则
    sort_weight INTEGER NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (product_id, field_key)
);
CREATE INDEX IF NOT EXISTS idx_product_custom_fields_product ON product_custom_fields(product_id);

-- ---------------------------------------------------------------------------
-- 4. 库存与限购（魔方：stock_control / qty / allow_qty / maximum_customer_purchase_quantity）
-- ---------------------------------------------------------------------------
ALTER TABLE products ADD COLUMN IF NOT EXISTS stock_control BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE products ADD COLUMN IF NOT EXISTS stock_qty INTEGER NOT NULL DEFAULT 0 CHECK (stock_qty >= 0);
ALTER TABLE products ADD COLUMN IF NOT EXISTS allow_qty BOOLEAN NOT NULL DEFAULT TRUE;      -- 允许一次买多件
ALTER TABLE products ADD COLUMN IF NOT EXISTS max_per_customer INTEGER NOT NULL DEFAULT 0;  -- 0 = 不限
ALTER TABLE products ADD COLUMN IF NOT EXISTS is_featured BOOLEAN NOT NULL DEFAULT FALSE;   -- 特色推荐

-- ---------------------------------------------------------------------------
-- 5. 订单/服务侧落库：买家选的配置项与自定义字段
-- ---------------------------------------------------------------------------
ALTER TABLE order_items ADD COLUMN IF NOT EXISTS config_cents BIGINT NOT NULL DEFAULT 0;
ALTER TABLE order_items ADD COLUMN IF NOT EXISTS setup_cents BIGINT NOT NULL DEFAULT 0;
ALTER TABLE order_items ADD COLUMN IF NOT EXISTS config_selections JSONB NOT NULL DEFAULT '[]'::jsonb;
ALTER TABLE order_items ADD COLUMN IF NOT EXISTS custom_fields JSONB NOT NULL DEFAULT '{}'::jsonb;

-- 已售数量：库存扣减与限购都基于它，避免每次下单去数订单
ALTER TABLE products ADD COLUMN IF NOT EXISTS sold_count INTEGER NOT NULL DEFAULT 0;
