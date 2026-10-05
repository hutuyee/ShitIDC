-- 流量包（对齐魔方 CBAP FlowPacket 插件）。
-- 后台维护流量包（名称 / 流量GB / 售价 / 库存 / 关联商品），用户端为名下关联产品购买；
-- 订单状态与插件一致：unpaid / paid / cancelled / refunded。

CREATE TABLE IF NOT EXISTS flow_packets (
    id BIGSERIAL PRIMARY KEY,
    public_id UUID NOT NULL DEFAULT gen_random_uuid() UNIQUE,
    name TEXT NOT NULL,
    capacity_gb INTEGER NOT NULL DEFAULT 0 CHECK (capacity_gb >= 0),
    price_cents BIGINT NOT NULL DEFAULT 0 CHECK (price_cents >= 0),
    stock INTEGER NOT NULL DEFAULT 0 CHECK (stock >= 0),
    stock_enable BOOLEAN NOT NULL DEFAULT FALSE,
    notes TEXT NOT NULL DEFAULT '',
    active BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS flow_packet_products (
    packet_id BIGINT NOT NULL REFERENCES flow_packets(id) ON DELETE CASCADE,
    product_id BIGINT NOT NULL REFERENCES products(id) ON DELETE CASCADE,
    PRIMARY KEY (packet_id, product_id)
);
CREATE INDEX IF NOT EXISTS idx_flow_packet_products_product ON flow_packet_products(product_id);

CREATE TABLE IF NOT EXISTS flow_packet_orders (
    id BIGSERIAL PRIMARY KEY,
    public_id UUID NOT NULL DEFAULT gen_random_uuid() UNIQUE,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    packet_id BIGINT REFERENCES flow_packets(id) ON DELETE SET NULL,
    packet_name TEXT NOT NULL DEFAULT '',
    capacity_gb INTEGER NOT NULL DEFAULT 0,
    service_id BIGINT REFERENCES services(id) ON DELETE SET NULL,
    service_name TEXT NOT NULL DEFAULT '',
    amount_cents BIGINT NOT NULL DEFAULT 0,
    status TEXT NOT NULL DEFAULT 'unpaid' CHECK (status IN ('unpaid','paid','cancelled','refunded')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    paid_at TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS idx_flow_packet_orders_user ON flow_packet_orders(user_id, id DESC);
CREATE INDEX IF NOT EXISTS idx_flow_packet_orders_status ON flow_packet_orders(status, id DESC);
CREATE INDEX IF NOT EXISTS idx_flow_packet_orders_service ON flow_packet_orders(service_id);

INSERT INTO permissions(name, description) VALUES
('flow_packet.manage','管理流量包（流量包配置 / 流量包订单）')
ON CONFLICT(name) DO NOTHING;

INSERT INTO role_permissions(role_id, permission_id)
SELECT r.id,p.id FROM roles r, permissions p
WHERE r.name='admin' AND p.name='flow_packet.manage'
ON CONFLICT DO NOTHING;
