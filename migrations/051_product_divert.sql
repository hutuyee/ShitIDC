-- 产品自助转移（对齐魔方主程序附属插件 product_divert）。
-- 用户 A 把名下产品转给用户 B：A 支付转出费用 → B 接收并支付转入费用 → 产品归属迁移。
-- 状态沿用插件 config.php 的字典：1 待接收 / 2 已完成 / 3 已关闭 / 4 已拒绝。
-- 双方费用各生成一张 kind='artificial' 的人工订单（kind_detail=divert_push / divert_pull），
-- 支付完成由人工订单收尾钩子推进转移状态（与发票费用单同模式）。

CREATE TABLE IF NOT EXISTS product_diverts (
    id BIGSERIAL PRIMARY KEY,
    public_id UUID NOT NULL DEFAULT gen_random_uuid() UNIQUE,
    service_id BIGINT NOT NULL REFERENCES services(id) ON DELETE RESTRICT,
    push_user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    pull_user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    status SMALLINT NOT NULL DEFAULT 1 CHECK (status IN (1, 2, 3, 4)),
    push_cost_cents BIGINT NOT NULL DEFAULT 0 CHECK (push_cost_cents >= 0),
    pull_cost_cents BIGINT NOT NULL DEFAULT 0 CHECK (pull_cost_cents >= 0),
    push_order_id BIGINT REFERENCES orders(id) ON DELETE SET NULL,
    pull_order_id BIGINT REFERENCES orders(id) ON DELETE SET NULL,
    push_paid_at TIMESTAMPTZ,
    pull_paid_at TIMESTAMPTZ,
    expires_at TIMESTAMPTZ,
    end_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_product_diverts_push ON product_diverts(push_user_id, id DESC);
CREATE INDEX IF NOT EXISTS idx_product_diverts_pull ON product_diverts(pull_user_id, id DESC);
CREATE INDEX IF NOT EXISTS idx_product_diverts_service ON product_diverts(service_id);
CREATE INDEX IF NOT EXISTS idx_product_diverts_pending ON product_diverts(status, expires_at) WHERE status = 1;
