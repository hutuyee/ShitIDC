-- 015: 短信通道与短信验证码（对应魔方 public/plugins/sms/ 的 Aliyun / Qcloudsms / Submail 等）。
--
-- 设计上与邮箱验证码保持一致：验证码只存哈希、按 (手机号, 用途) 限频、错 5 次锁定、
-- 过期自动清理。差别是短信要花钱，所以另外记录发送流水，便于排查"被盗刷"。

-- 1. 短信通道配置。一个站点可以配多个通道，用 is_default 选默认的。
CREATE TABLE IF NOT EXISTS sms_providers (
    id BIGSERIAL PRIMARY KEY,
    public_id UUID NOT NULL DEFAULT gen_random_uuid() UNIQUE,
    name TEXT NOT NULL,
    -- 通道实现标识：aliyun / qcloud / submail / custom_http
    provider TEXT NOT NULL,
    -- 通道自己的接入参数（签名、模板 ID、地域等），不含密钥
    config JSONB NOT NULL DEFAULT '{}'::jsonb,
    -- 加密后的 AccessKey / SecretKey（JSON），仅服务端解密使用
    secret_encrypted TEXT NOT NULL DEFAULT '',
    active BOOLEAN NOT NULL DEFAULT TRUE,
    is_default BOOLEAN NOT NULL DEFAULT FALSE,
    last_ok_at TIMESTAMPTZ,
    last_error TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- 同一时间只允许一个默认通道。
CREATE UNIQUE INDEX IF NOT EXISTS ux_sms_providers_default
    ON sms_providers(is_default) WHERE is_default = TRUE;

-- 2. 短信验证码。结构与 email_verification_codes 对齐。
CREATE TABLE IF NOT EXISTS sms_verification_codes (
    id BIGSERIAL PRIMARY KEY,
    phone TEXT NOT NULL,
    purpose TEXT NOT NULL,
    code_hash TEXT NOT NULL,
    attempts INTEGER NOT NULL DEFAULT 0,
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (phone, purpose)
);
CREATE INDEX IF NOT EXISTS idx_sms_codes_created ON sms_verification_codes(created_at);

-- 3. 发送流水：短信是花钱的，必须能回答"谁在什么时候给哪个号发了多少条"。
CREATE TABLE IF NOT EXISTS sms_messages (
    id BIGSERIAL PRIMARY KEY,
    phone TEXT NOT NULL,
    purpose TEXT NOT NULL,
    provider TEXT NOT NULL DEFAULT '',
    template TEXT NOT NULL DEFAULT '',
    ok BOOLEAN NOT NULL DEFAULT FALSE,
    error TEXT NOT NULL DEFAULT '',
    user_id BIGINT REFERENCES users(id) ON DELETE SET NULL,
    ip TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_sms_messages_phone_time ON sms_messages(phone, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_sms_messages_created ON sms_messages(created_at DESC);

-- 4. 用户手机号与手机验证状态（魔方有 is_bind_phone；这里补齐等价能力）。
ALTER TABLE users ADD COLUMN IF NOT EXISTS phone TEXT NOT NULL DEFAULT '';
ALTER TABLE users ADD COLUMN IF NOT EXISTS phone_verified BOOLEAN NOT NULL DEFAULT FALSE;
CREATE UNIQUE INDEX IF NOT EXISTS ux_users_phone ON users(phone) WHERE phone <> '';
