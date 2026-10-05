-- 产品转移（对齐魔方 CBAP HostTransfer 插件）。
-- 记录每次迁移的产品与双方用户；操作走服务管理权限（service.manage）。

CREATE TABLE IF NOT EXISTS service_transfers (
    id BIGSERIAL PRIMARY KEY,
    service_id BIGINT NOT NULL REFERENCES services(id) ON DELETE CASCADE,
    from_user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    to_user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    operator_id BIGINT REFERENCES users(id) ON DELETE SET NULL,
    remark TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_service_transfers_service ON service_transfers(service_id, id DESC);
CREATE INDEX IF NOT EXISTS idx_service_transfers_created ON service_transfers(created_at DESC);
