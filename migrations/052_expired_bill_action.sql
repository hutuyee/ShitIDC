-- 到期账单处理（对齐魔方主程序附属插件 expired_auto_delete_bill）。
-- 产品（服务）被终止时，把该服务未支付的续费订单 / 账单按配置处理（插件语义：
-- 直接删除 / 标记取消），并逐笔留档供后台查询。
-- 站内账目不物理删除：「直接删除」与「标记取消」的最终账面状态都是 void（作废），
-- 差异保留在处理记录的 action 列里。

CREATE TABLE IF NOT EXISTS expired_bill_logs (
    id BIGSERIAL PRIMARY KEY,
    public_id UUID NOT NULL DEFAULT gen_random_uuid() UNIQUE,
    invoice_id BIGINT REFERENCES invoices(id) ON DELETE SET NULL,
    invoice_public_id TEXT NOT NULL DEFAULT '',
    invoice_status TEXT NOT NULL DEFAULT '',
    action TEXT NOT NULL CHECK (action IN ('delete', 'cancel')),
    service_id BIGINT REFERENCES services(id) ON DELETE SET NULL,
    service_public_id TEXT NOT NULL DEFAULT '',
    user_id BIGINT REFERENCES users(id) ON DELETE SET NULL,
    product_name TEXT NOT NULL DEFAULT '',
    dedicated_ip TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_expired_bill_logs_created ON expired_bill_logs(created_at DESC);
CREATE INDEX IF NOT EXISTS idx_expired_bill_logs_service ON expired_bill_logs(service_id);
