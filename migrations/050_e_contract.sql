-- 电子合同（对齐魔方 CBAP EContract 插件）。
-- 合同模板（关联商品 / 变量 / 基础合同 / 强制签订）、合同申请（用户对已支付
-- 订单申请，编号自动递增）、用户签订（签名图）、后台审核（通过 / 驳回 / 作废 /
-- 邮寄登记）、合同文件下载（可打印 HTML，替代插件的 PDF 生成）。
-- 插件对接的第三方电子签通道（加密不可读）不落地，签订为站内流程。

CREATE TABLE IF NOT EXISTS e_contract_templates (
    id BIGSERIAL PRIMARY KEY,
    public_id UUID NOT NULL DEFAULT gen_random_uuid() UNIQUE,
    name TEXT NOT NULL,
    detail TEXT NOT NULL DEFAULT '',
    base_contract BOOLEAN NOT NULL DEFAULT FALSE,
    force_sign BOOLEAN NOT NULL DEFAULT FALSE,
    notes TEXT NOT NULL DEFAULT '',
    active BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS e_contract_template_products (
    template_id BIGINT NOT NULL REFERENCES e_contract_templates(id) ON DELETE CASCADE,
    product_id BIGINT NOT NULL REFERENCES products(id) ON DELETE CASCADE,
    PRIMARY KEY (template_id, product_id)
);

CREATE TABLE IF NOT EXISTS e_contracts (
    id BIGSERIAL PRIMARY KEY,
    public_id UUID NOT NULL DEFAULT gen_random_uuid() UNIQUE,
    number TEXT NOT NULL DEFAULT '',
    template_id BIGINT REFERENCES e_contract_templates(id) ON DELETE SET NULL,
    template_name TEXT NOT NULL DEFAULT '',
    order_id BIGINT REFERENCES orders(id) ON DELETE SET NULL,
    user_id BIGINT REFERENCES users(id) ON DELETE SET NULL,
    content TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','signed','effective','reject','cancel')),
    sign_image TEXT NOT NULL DEFAULT '',
    signed_at TIMESTAMPTZ,
    reason TEXT NOT NULL DEFAULT '',
    courier_company TEXT NOT NULL DEFAULT '',
    courier_number TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_e_contracts_user ON e_contracts(user_id, id DESC);
CREATE INDEX IF NOT EXISTS idx_e_contracts_status ON e_contracts(status);
CREATE UNIQUE INDEX IF NOT EXISTS idx_e_contracts_number ON e_contracts(number) WHERE number <> '';

INSERT INTO permissions(name, description) VALUES
('e_contract.manage','管理电子合同（模板 / 审核 / 邮寄 / 设置）')
ON CONFLICT(name) DO NOTHING;

INSERT INTO role_permissions(role_id, permission_id)
SELECT r.id,p.id FROM roles r, permissions p
WHERE r.name='admin' AND p.name='e_contract.manage'
ON CONFLICT DO NOTHING;
