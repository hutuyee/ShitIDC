-- 046: 推介计划（对齐魔方 CBAP IdcsmartRecommend 插件）。
-- 推介人在会员中心开启推介计划；被推介用户下单支付成功后按商品比例生成奖励记录
-- （新购 / 续费两套比例与最低金额），经确认天数后转为已确认（可提现）；
-- 后台可确认 / 冻结 / 解冻 / 置无效 / 改奖励金额 / 删除，并维护预设无效回复；
-- 用户可生成自定义推介链接、申请提现，后台审核打款。
-- 标量配置存 system_settings.recommend（awards_cents / confirm_days /
-- withdraw_min_cents / withdraw_handling_fee / system_urls / default_url）。

-- 1) 商品奖励比例（阈值按同一订单同商品合计金额计算） ---------------------------

CREATE TABLE IF NOT EXISTS recommend_ratios (
    id BIGSERIAL PRIMARY KEY,
    product_id BIGINT NOT NULL REFERENCES products(id) ON DELETE CASCADE UNIQUE,
    ratio NUMERIC(6,2) NOT NULL DEFAULT 0 CHECK (ratio >= 0),
    amount_cents BIGINT NOT NULL DEFAULT 0 CHECK (amount_cents >= 0),
    renew_ratio NUMERIC(6,2) NOT NULL DEFAULT 0 CHECK (renew_ratio >= 0),
    renew_amount_cents BIGINT NOT NULL DEFAULT 0 CHECK (renew_amount_cents >= 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- 2) 主打推介产品（会员中心「建议推介产品」，按 sort 排序） ----------------------

CREATE TABLE IF NOT EXISTS recommend_products (
    id BIGSERIAL PRIMARY KEY,
    product_id BIGINT NOT NULL REFERENCES products(id) ON DELETE CASCADE UNIQUE,
    sort INT NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- 3) 推介人（开启推介计划后才有记录；首次开启发放初始奖励存款） -----------------

CREATE TABLE IF NOT EXISTS recommend_promoters (
    user_id BIGINT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- 4) 自定义推介链接（系统页面 + 自定义后缀） ------------------------------------

CREATE TABLE IF NOT EXISTS recommend_links (
    id BIGSERIAL PRIMARY KEY,
    promoter_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    system_url TEXT NOT NULL,
    custom_url TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (promoter_id, system_url, custom_url)
);

-- 5) 奖励记录：Pending 待确认 / Active 已确认 / Frozen 冻结 / Invalid 无效 -------

CREATE TABLE IF NOT EXISTS recommend_awards (
    id BIGSERIAL PRIMARY KEY,
    public_id UUID NOT NULL DEFAULT gen_random_uuid() UNIQUE,
    promoter_id BIGINT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    user_id BIGINT REFERENCES users(id) ON DELETE SET NULL,
    order_id BIGINT REFERENCES orders(id) ON DELETE SET NULL,
    product_id BIGINT REFERENCES products(id) ON DELETE SET NULL,
    product_name TEXT NOT NULL DEFAULT '',
    type TEXT NOT NULL DEFAULT 'new' CHECK (type IN ('new','renew','init')),
    buy_amount_cents BIGINT NOT NULL DEFAULT 0 CHECK (buy_amount_cents >= 0),
    ratio NUMERIC(6,2) NOT NULL DEFAULT 0 CHECK (ratio >= 0),
    awards_amount_cents BIGINT NOT NULL DEFAULT 0 CHECK (awards_amount_cents >= 0),
    status TEXT NOT NULL DEFAULT 'Pending' CHECK (status IN ('Pending','Active','Frozen','Invalid')),
    invalid_reason TEXT NOT NULL DEFAULT '',
    confirm_at TIMESTAMPTZ,
    active_at TIMESTAMPTZ,
    idempotency_key TEXT UNIQUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_recommend_awards_promoter ON recommend_awards(promoter_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_recommend_awards_status ON recommend_awards(status, confirm_at);

-- 6) 提现记录：0 待审核 / 1 待打款 / 2 审核驳回 / 3 已打款 ----------------------

CREATE TABLE IF NOT EXISTS recommend_withdrawals (
    id BIGSERIAL PRIMARY KEY,
    public_id UUID NOT NULL DEFAULT gen_random_uuid() UNIQUE,
    promoter_id BIGINT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    amount_cents BIGINT NOT NULL CHECK (amount_cents > 0),
    fee_cents BIGINT NOT NULL DEFAULT 0 CHECK (fee_cents >= 0),
    method TEXT NOT NULL DEFAULT '',
    status INT NOT NULL DEFAULT 0 CHECK (status BETWEEN 0 AND 3),
    reason TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_recommend_withdrawals_promoter ON recommend_withdrawals(promoter_id, created_at DESC);

-- 7) 预设无效回复（system=TRUE 为内置项，不可编辑 / 删除） ---------------------

CREATE TABLE IF NOT EXISTS recommend_prereplies (
    id BIGSERIAL PRIMARY KEY,
    content TEXT NOT NULL,
    system BOOLEAN NOT NULL DEFAULT FALSE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
INSERT INTO recommend_prereplies(content,system)
SELECT '该推介记录不符合推介奖励规则。',TRUE
WHERE NOT EXISTS (SELECT 1 FROM recommend_prereplies);
