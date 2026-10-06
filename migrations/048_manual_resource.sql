-- 手动资源（对齐魔方 CBAP ManualResource 插件）。
-- 独立服务器台账：供应商、资源（主 IP / 附加 IP / 配置 / 成本 / 系统账密 /
-- 控制方式 ipmi|client / IPMI 或 DCIM 客户端参数 / 到期时间）与电源状态，
-- 可分配（关联）到用户名下的产品，IPMI 模式支持电源操作（internal/ipmi）。

CREATE TABLE IF NOT EXISTS manual_suppliers (
    id BIGSERIAL PRIMARY KEY,
    public_id UUID NOT NULL DEFAULT gen_random_uuid() UNIQUE,
    name TEXT NOT NULL,
    contact TEXT NOT NULL DEFAULT '',
    notes TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS manual_resources (
    id BIGSERIAL PRIMARY KEY,
    public_id UUID NOT NULL DEFAULT gen_random_uuid() UNIQUE,
    dedicated_ip TEXT NOT NULL DEFAULT '',
    assigned_ips TEXT NOT NULL DEFAULT '',
    notes TEXT NOT NULL DEFAULT '',
    configuration TEXT NOT NULL DEFAULT '',
    cost_cents BIGINT NOT NULL DEFAULT 0 CHECK (cost_cents >= 0),
    username TEXT NOT NULL DEFAULT '',
    password TEXT NOT NULL DEFAULT '',
    control_mode TEXT NOT NULL DEFAULT 'ipmi' CHECK (control_mode IN ('ipmi','client')),
    ipmi_ip TEXT NOT NULL DEFAULT '',
    ipmi_port INTEGER NOT NULL DEFAULT 623 CHECK (ipmi_port > 0 AND ipmi_port < 65536),
    ipmi_version TEXT NOT NULL DEFAULT '',
    dcim_client_url TEXT NOT NULL DEFAULT '',
    dcim_client_id TEXT NOT NULL DEFAULT '',
    control_username TEXT NOT NULL DEFAULT '',
    control_password TEXT NOT NULL DEFAULT '',
    due_time TIMESTAMPTZ,
    supplier_id BIGINT REFERENCES manual_suppliers(id) ON DELETE SET NULL,
    user_id BIGINT REFERENCES users(id) ON DELETE SET NULL,
    service_id BIGINT REFERENCES services(id) ON DELETE SET NULL,
    status TEXT NOT NULL DEFAULT 'idle' CHECK (status IN ('idle','assigned')),
    power_status TEXT NOT NULL DEFAULT '' CHECK (power_status IN ('','on','off','error')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_manual_resources_dedicated_ip ON manual_resources(dedicated_ip);
CREATE INDEX IF NOT EXISTS idx_manual_resources_user ON manual_resources(user_id);

INSERT INTO permissions(name, description) VALUES
('manual_resource.manage','管理手动资源（供应商 / 资源台账 / 电源操作）')
ON CONFLICT(name) DO NOTHING;

INSERT INTO role_permissions(role_id, permission_id)
SELECT r.id,p.id FROM roles r, permissions p
WHERE r.name='admin' AND p.name='manual_resource.manage'
ON CONFLICT DO NOTHING;
