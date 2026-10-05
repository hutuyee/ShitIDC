-- 034: 商品购买限制（对齐魔方 CBAP 插件 product_cert_limit / product_cycle_limit /
-- product_related_limit）。
--
-- 三个插件在前端契约里各有独立的列表 / 新增 / 编辑 / 启停 / 删除接口，这里落成
-- 三张限制表：实名要求、周期性限购、关联限购。
-- ProductNumLimit 插件（单客户数量限制）与站内商品自带的「单客户最多购买」
-- （products.max_per_customer，见 checkStockAndQtyTx）等价，不再重复建表。
--
-- 计数口径与插件语言包一致：用户账户中的服务，状态「已删除 / 已取消」
--（本站对应 terminated / failed）不计数，其余状态均计数。

CREATE TABLE IF NOT EXISTS product_cert_limits (
    id BIGSERIAL PRIMARY KEY,
    public_id UUID NOT NULL DEFAULT gen_random_uuid() UNIQUE,
    product_id BIGINT NOT NULL REFERENCES products(id) ON DELETE CASCADE UNIQUE,
    -- 插件取值：1 个人/企业、2 个人认证、3 企业认证。
    -- 本站实名为统一类型（不区分个人/企业），三种取值都按「已通过实名」校验。
    type SMALLINT NOT NULL DEFAULT 1 CHECK (type IN (1,2,3)),
    status BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS product_cycle_limits (
    id BIGSERIAL PRIMARY KEY,
    public_id UUID NOT NULL DEFAULT gen_random_uuid() UNIQUE,
    product_id BIGINT NOT NULL REFERENCES products(id) ON DELETE CASCADE UNIQUE,
    -- 周期内最多拥有的件数。
    num INT NOT NULL CHECK (num >= 1),
    -- 限制周期（天）；0 = 永久限制。
    cycle INT NOT NULL DEFAULT 0 CHECK (cycle >= 0),
    status BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS product_related_limits (
    id BIGSERIAL PRIMARY KEY,
    public_id UUID NOT NULL DEFAULT gen_random_uuid() UNIQUE,
    product_id BIGINT NOT NULL REFERENCES products(id) ON DELETE CASCADE,
    related_product_ids BIGINT[] NOT NULL,
    -- 0 捆绑（须同单购买）/ 1 必需（需已拥有激活中的关联商品）/ 2 互斥（不得拥有）。
    type SMALLINT NOT NULL DEFAULT 0 CHECK (type IN (0,1,2)),
    status BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_product_related_limits_product ON product_related_limits(product_id);
