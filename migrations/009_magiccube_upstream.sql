-- 009: 对接魔方（ZJMF / 智简魔方）上游访问密钥。
--
-- 魔方财务的"服务器模块"（接口插件）按固定协议调用上游：请求体带
-- time / random / signature，签名算法取自魔方财务 3.7.6 明文参考模块
-- public/plugins/servers/bthosts/bthosts.php:14-23：
--
--     $data = ['time' => $time, 'random' => $random, 'token' => $token];
--     sort($data, SORT_STRING);
--     $signature = strtoupper(md5(implode($data)));
--
-- 其中 token 从不随请求发送，只参与摘要；它由管理端在魔方"接口设置"里填到
-- Hash(accesshash) 字段。因为要验证签名就必须拿到 token 原文，而用户级
-- api_tokens 只保存 HMAC（无法反推），所以魔方对接单独用这张表：密钥用
-- MASTER_KEY 做 AES-256-GCM 加密存储，仅在服务端解密用于验签。
--
-- 一个密钥归属某个 ShitIDC 用户；魔方通过它开通出来的服务都记在该用户名下，
-- 开通/续费也从这个用户的余额扣费。
CREATE TABLE IF NOT EXISTS upstream_keys (
    id BIGSERIAL PRIMARY KEY,
    public_id UUID NOT NULL DEFAULT gen_random_uuid() UNIQUE,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    key_id TEXT NOT NULL UNIQUE,                      -- 明文标识，便于在魔方侧辨认
    secret_enc TEXT NOT NULL,                         -- MASTER_KEY 加密的签名密钥
    scopes TEXT[] NOT NULL DEFAULT '{product.read,service.read,service.operate,order.read,order.write}',
    active BOOLEAN NOT NULL DEFAULT TRUE,
    last_used_at TIMESTAMPTZ,
    last_used_ip INET,
    last_error TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_upstream_keys_user ON upstream_keys(user_id);

-- 记录魔方侧的主机 ID 与本地服务的对应关系，便于反查（服务本身也用
-- provider_ref 存上游 ID，这里额外留痕魔方下发的 hostid/user_id，用于工单排查）。
CREATE TABLE IF NOT EXISTS upstream_host_links (
    id BIGSERIAL PRIMARY KEY,
    upstream_key_id BIGINT NOT NULL REFERENCES upstream_keys(id) ON DELETE CASCADE,
    service_public_id UUID NOT NULL,
    upstream_host_id TEXT NOT NULL DEFAULT '',
    upstream_user_id TEXT NOT NULL DEFAULT '',
    upstream_domain TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (upstream_key_id, service_public_id)
);
CREATE INDEX IF NOT EXISTS idx_upstream_host_links_service ON upstream_host_links(service_public_id);

-- 修复 008 的优惠券 NULL 边界：product_ids 有 NOT NULL DEFAULT '{}'，但没有
-- DEFAULT 时显式传 NULL 仍会被拒绝（历史数据里若已有 NULL 也要补回来）。
ALTER TABLE coupons ALTER COLUMN product_ids SET DEFAULT '{}';
UPDATE coupons SET product_ids = '{}' WHERE product_ids IS NULL;
