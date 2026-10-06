-- 万云资源管理（对齐魔方 CBAP WanyunResource 插件）。
-- IDC 机房资源台账：IP 段（含子网与地址明细）、节点（含类型与自定义字段）、
-- VLAN（含类型）、光纤与纤芯（含途径节点与纤芯自定义字段）。
-- 插件的 DCIM 接口同步（加密协议不可读）不落地，数据全部手工维护。

CREATE TABLE IF NOT EXISTS wy_ip_segments (
    id BIGSERIAL PRIMARY KEY,
    public_id UUID NOT NULL DEFAULT gen_random_uuid() UNIQUE,
    parent_id BIGINT REFERENCES wy_ip_segments(id) ON DELETE CASCADE,
    name TEXT NOT NULL DEFAULT '',
    subnet TEXT NOT NULL DEFAULT '',
    subnet_mask TEXT NOT NULL DEFAULT '',
    gateway TEXT NOT NULL DEFAULT '',
    group_name TEXT NOT NULL DEFAULT '',
    notes TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_wy_ip_segments_parent ON wy_ip_segments(parent_id);

CREATE TABLE IF NOT EXISTS wy_ip_addresses (
    id BIGSERIAL PRIMARY KEY,
    public_id UUID NOT NULL DEFAULT gen_random_uuid() UNIQUE,
    segment_id BIGINT NOT NULL REFERENCES wy_ip_segments(id) ON DELETE CASCADE,
    ip TEXT NOT NULL,
    assignor TEXT NOT NULL DEFAULT '',
    username TEXT NOT NULL DEFAULT '',
    use_unit TEXT NOT NULL DEFAULT '',
    notes TEXT NOT NULL DEFAULT '',
    used BOOLEAN NOT NULL DEFAULT FALSE,
    used_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_wy_ip_addresses_segment ON wy_ip_addresses(segment_id);

CREATE TABLE IF NOT EXISTS wy_node_types (
    id BIGSERIAL PRIMARY KEY,
    public_id UUID NOT NULL DEFAULT gen_random_uuid() UNIQUE,
    name TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS wy_nodes (
    id BIGSERIAL PRIMARY KEY,
    public_id UUID NOT NULL DEFAULT gen_random_uuid() UNIQUE,
    name TEXT NOT NULL,
    type_id BIGINT REFERENCES wy_node_types(id) ON DELETE SET NULL,
    notes TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- 节点 / 纤芯自定义字段（text / dropdown，必填、列表展示开关、拖动排序）。
CREATE TABLE IF NOT EXISTS wy_custom_fields (
    id BIGSERIAL PRIMARY KEY,
    public_id UUID NOT NULL DEFAULT gen_random_uuid() UNIQUE,
    scope TEXT NOT NULL CHECK (scope IN ('node','fiber_core')),
    field_name TEXT NOT NULL,
    field_type TEXT NOT NULL DEFAULT 'text' CHECK (field_type IN ('text','dropdown')),
    field_option TEXT NOT NULL DEFAULT '',
    is_required BOOLEAN NOT NULL DEFAULT FALSE,
    show_list BOOLEAN NOT NULL DEFAULT FALSE,
    sort_weight INTEGER NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_wy_custom_fields_scope ON wy_custom_fields(scope, sort_weight);

CREATE TABLE IF NOT EXISTS wy_custom_field_values (
    field_id BIGINT NOT NULL REFERENCES wy_custom_fields(id) ON DELETE CASCADE,
    object_id BIGINT NOT NULL,
    value TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (field_id, object_id)
);

CREATE TABLE IF NOT EXISTS wy_vlan_types (
    id BIGSERIAL PRIMARY KEY,
    public_id UUID NOT NULL DEFAULT gen_random_uuid() UNIQUE,
    name TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS wy_vlans (
    id BIGSERIAL PRIMARY KEY,
    public_id UUID NOT NULL DEFAULT gen_random_uuid() UNIQUE,
    vlan_id INTEGER NOT NULL,
    name TEXT NOT NULL,
    type_id BIGINT REFERENCES wy_vlan_types(id) ON DELETE SET NULL,
    assignor TEXT NOT NULL DEFAULT '',
    username TEXT NOT NULL DEFAULT '',
    use_unit TEXT NOT NULL DEFAULT '',
    notes TEXT NOT NULL DEFAULT '',
    active BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_wy_vlans_vlan_id ON wy_vlans(vlan_id);

CREATE TABLE IF NOT EXISTS wy_vlan_nodes (
    vlan_id BIGINT NOT NULL REFERENCES wy_vlans(id) ON DELETE CASCADE,
    node_id BIGINT NOT NULL REFERENCES wy_nodes(id) ON DELETE CASCADE,
    PRIMARY KEY (vlan_id, node_id)
);

CREATE TABLE IF NOT EXISTS wy_fibers (
    id BIGSERIAL PRIMARY KEY,
    public_id UUID NOT NULL DEFAULT gen_random_uuid() UNIQUE,
    fiber_num TEXT NOT NULL,
    owner TEXT NOT NULL DEFAULT '',
    core_num INTEGER NOT NULL DEFAULT 0 CHECK (core_num >= 0),
    open_unit TEXT NOT NULL DEFAULT '',
    construction_unit TEXT NOT NULL DEFAULT '',
    contact TEXT NOT NULL DEFAULT '',
    project TEXT NOT NULL DEFAULT '',
    price_cents BIGINT NOT NULL DEFAULT 0 CHECK (price_cents >= 0),
    notes TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS wy_fiber_nodes (
    fiber_id BIGINT NOT NULL REFERENCES wy_fibers(id) ON DELETE CASCADE,
    node_id BIGINT NOT NULL REFERENCES wy_nodes(id) ON DELETE CASCADE,
    PRIMARY KEY (fiber_id, node_id)
);

CREATE TABLE IF NOT EXISTS wy_fiber_cores (
    id BIGSERIAL PRIMARY KEY,
    public_id UUID NOT NULL DEFAULT gen_random_uuid() UNIQUE,
    fiber_id BIGINT NOT NULL REFERENCES wy_fibers(id) ON DELETE CASCADE,
    num TEXT NOT NULL DEFAULT '',
    notes TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_wy_fiber_cores_fiber ON wy_fiber_cores(fiber_id);

CREATE TABLE IF NOT EXISTS wy_fiber_core_nodes (
    core_id BIGINT NOT NULL REFERENCES wy_fiber_cores(id) ON DELETE CASCADE,
    node_id BIGINT NOT NULL REFERENCES wy_nodes(id) ON DELETE CASCADE,
    PRIMARY KEY (core_id, node_id)
);

INSERT INTO permissions(name, description) VALUES
('wanyun_resource.manage','管理万云资源（IP 段 / 节点 / VLAN / 光纤 / 纤芯）')
ON CONFLICT(name) DO NOTHING;

INSERT INTO role_permissions(role_id, permission_id)
SELECT r.id,p.id FROM roles r, permissions p
WHERE r.name='admin' AND p.name='wanyun_resource.manage'
ON CONFLICT DO NOTHING;
