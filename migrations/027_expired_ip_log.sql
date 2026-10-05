-- 027_expired_ip_log.sql —— 对齐魔方附属插件 expired_ip_log（到期产品删除IP记录）。
--
-- 参考插件挂 afterModuleTerminate：产品删除时把 host 的 dedicatedip /
-- assignedips / regdate / uid 写入 shd_expired_ip_log。ShitIDC 没有 host 表：
-- 独立 IP 来自开通时供应商返回、写在 services.provider_payload 的实例数据
-- （例如 NOKVM 的 main_ip / assigned_ips）。
CREATE TABLE IF NOT EXISTS expired_ip_logs (
    id BIGSERIAL PRIMARY KEY,
    service_id TEXT NOT NULL,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    product_name TEXT NOT NULL DEFAULT '',
    dedicated_ip TEXT NOT NULL DEFAULT '',
    assigned_ips TEXT NOT NULL DEFAULT '',
    service_created_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_expired_ip_logs_created ON expired_ip_logs(created_at DESC);
CREATE INDEX IF NOT EXISTS idx_expired_ip_logs_service ON expired_ip_logs(service_id);
