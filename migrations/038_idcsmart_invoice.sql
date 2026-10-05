-- 038: 发票申请（对齐魔方 CBAP 插件 IdcsmartInvoice）。
--
-- 插件可读的前端契约（template/{admin,clientarea}/{api,js,lang,*.html}）：
--   * 发票抬头（invoice_title）：title_type 公司/个人、title、invoice_type
--     增值税普通 normal / 专用 special、公司地址、税务登记号、开户行、开户账号。
--   * 收件地址（invoice_address）：rec_type 纸质 paper / 电子 email、收件人、
--     省市区 + 详细地址、电话、邮箱、收件网址、默认地址。
--   * 发票项目（invoice_project）：名称 + 普票税率 / 普票收税金额 / 专票开关 +
--     专票税率 / 专票收税金额（税率用于票面展示，收税金额是向客户收取的税金比例）。
--   * 发票设置（invoice_config）：invoice_manage 开关、pre_invoice 允许未支付订单
--     申请、across_year_invoice 允许开往年发票、快递方式列表（名称 + 价格）。
--   * 发票申请（invoice）：选择订单 + 抬头 + 收件信息 + 项目 + 格式（pdf/ofd/xml），
--     状态 pending 待审核 / unpaid 待支付（税金 + 快递费）/ wait_send 待发出 /
--     sent 已发出 / reject 已驳回 / cancel 作废 / flushed 已冲红；后台支持通过、
--     驳回、发出（电子票上传文件、纸质票快递单号 + 快递单照片）、冲红、删除文件。
--
-- 站内落地：
--   * 发票与订单通过 invoice_request_orders 关联（同一订单在有效申请期间不可重复开票）。
--   * 需支付的税金 / 快递费生成一张 kind='artificial'、kind_detail='invoice_fee' 的
--     费用单（支付后不开通服务），支付成功由结算流程把申请推进到「待审核」。
--   * 金额一律落「分」，税率 / 收税比例落基点（100 = 1%）。

CREATE TABLE IF NOT EXISTS invoice_titles (
    id BIGSERIAL PRIMARY KEY,
    public_id UUID NOT NULL DEFAULT gen_random_uuid() UNIQUE,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    title_type TEXT NOT NULL CHECK (title_type IN ('company','person')),
    title TEXT NOT NULL,
    invoice_type TEXT NOT NULL CHECK (invoice_type IN ('normal','special')),
    company_address TEXT NOT NULL DEFAULT '',
    tax TEXT NOT NULL DEFAULT '',
    bank TEXT NOT NULL DEFAULT '',
    bank_user TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_invoice_titles_user ON invoice_titles(user_id, id DESC);

CREATE TABLE IF NOT EXISTS invoice_addresses (
    id BIGSERIAL PRIMARY KEY,
    public_id UUID NOT NULL DEFAULT gen_random_uuid() UNIQUE,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    rec_type TEXT NOT NULL CHECK (rec_type IN ('paper','email')),
    rec_name TEXT NOT NULL DEFAULT '',
    province TEXT NOT NULL DEFAULT '',
    city TEXT NOT NULL DEFAULT '',
    region TEXT NOT NULL DEFAULT '',
    address TEXT NOT NULL DEFAULT '',
    phone TEXT NOT NULL DEFAULT '',
    is_default BOOLEAN NOT NULL DEFAULT FALSE,
    email TEXT NOT NULL DEFAULT '',
    rec_url TEXT NOT NULL DEFAULT '',
    notes TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_invoice_addresses_user ON invoice_addresses(user_id, id DESC);

CREATE TABLE IF NOT EXISTS invoice_projects (
    id BIGSERIAL PRIMARY KEY,
    public_id UUID NOT NULL DEFAULT gen_random_uuid() UNIQUE,
    name TEXT NOT NULL,
    normal_tax_rate_bp INTEGER NOT NULL DEFAULT 0 CHECK (normal_tax_rate_bp >= 0 AND normal_tax_rate_bp <= 10000),
    normal_tax_fee_bp INTEGER NOT NULL DEFAULT 0 CHECK (normal_tax_fee_bp >= 0 AND normal_tax_fee_bp <= 10000),
    special_tax_switch BOOLEAN NOT NULL DEFAULT FALSE,
    special_tax_rate_bp INTEGER NOT NULL DEFAULT 0 CHECK (special_tax_rate_bp >= 0 AND special_tax_rate_bp <= 10000),
    special_tax_fee_bp INTEGER NOT NULL DEFAULT 0 CHECK (special_tax_fee_bp >= 0 AND special_tax_fee_bp <= 10000),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS invoice_requests (
    id BIGSERIAL PRIMARY KEY,
    public_id UUID NOT NULL DEFAULT gen_random_uuid() UNIQUE,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','unpaid','wait_send','sent','reject','cancel','flushed')),
    title_type TEXT NOT NULL DEFAULT 'company',
    title TEXT NOT NULL,
    invoice_type TEXT NOT NULL DEFAULT 'normal' CHECK (invoice_type IN ('normal','special')),
    tax TEXT NOT NULL DEFAULT '',
    company_address TEXT NOT NULL DEFAULT '',
    bank TEXT NOT NULL DEFAULT '',
    bank_user TEXT NOT NULL DEFAULT '',
    rec_type TEXT NOT NULL CHECK (rec_type IN ('paper','email')),
    rec_name TEXT NOT NULL DEFAULT '',
    rec_address TEXT NOT NULL DEFAULT '',
    rec_phone TEXT NOT NULL DEFAULT '',
    rec_email TEXT NOT NULL DEFAULT '',
    rec_url TEXT NOT NULL DEFAULT '',
    invoice_format TEXT NOT NULL DEFAULT 'pdf',
    invoice_project TEXT NOT NULL DEFAULT '',
    tax_rate_bp INTEGER NOT NULL DEFAULT 0,
    tax_fee_bp INTEGER NOT NULL DEFAULT 0,
    amount_cents BIGINT NOT NULL DEFAULT 0 CHECK (amount_cents >= 0),
    tax_cents BIGINT NOT NULL DEFAULT 0 CHECK (tax_cents >= 0),
    parcel_name TEXT NOT NULL DEFAULT '',
    parcel_price_cents BIGINT NOT NULL DEFAULT 0 CHECK (parcel_price_cents >= 0),
    total_cents BIGINT NOT NULL DEFAULT 0 CHECK (total_cents >= 0),
    fee_cents BIGINT NOT NULL DEFAULT 0 CHECK (fee_cents >= 0),
    fee_order_id BIGINT REFERENCES orders(id) ON DELETE SET NULL,
    parcel_number TEXT NOT NULL DEFAULT '',
    review_notes TEXT NOT NULL DEFAULT '',
    reject_reason TEXT NOT NULL DEFAULT '',
    invoice_filename TEXT NOT NULL DEFAULT '',
    sent_at TIMESTAMPTZ,
    flushed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_invoice_requests_user ON invoice_requests(user_id, id DESC);
CREATE INDEX IF NOT EXISTS idx_invoice_requests_status ON invoice_requests(status, id DESC);

CREATE TABLE IF NOT EXISTS invoice_request_orders (
    invoice_id BIGINT NOT NULL REFERENCES invoice_requests(id) ON DELETE CASCADE,
    order_id BIGINT NOT NULL REFERENCES orders(id) ON DELETE RESTRICT,
    PRIMARY KEY (invoice_id, order_id)
);
CREATE INDEX IF NOT EXISTS idx_invoice_request_orders_order ON invoice_request_orders(order_id);

INSERT INTO permissions(name, description) VALUES
('invoice.manage','管理发票申请（审核/驳回/发出/冲红/配置/项目）')
ON CONFLICT(name) DO NOTHING;

INSERT INTO role_permissions(role_id, permission_id)
SELECT r.id,p.id FROM roles r, permissions p
WHERE r.name='admin' AND p.name='invoice.manage'
ON CONFLICT DO NOTHING;
