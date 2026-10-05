-- 029: 对象存储通道（对应魔方 public/plugins/oss/ 的 TencentcloudOss）。
--
-- 与 sms / mail / captcha 通道同构：多条配置、一个默认、凭据加密入库。
-- 未配置任何通道时上传仍落本机 data/uploads，默认行为不变。

CREATE TABLE IF NOT EXISTS oss_providers (
    id BIGSERIAL PRIMARY KEY,
    public_id UUID NOT NULL DEFAULT gen_random_uuid() UNIQUE,
    name TEXT NOT NULL,
    -- 通道实现标识：tencentcloud_oss
    provider TEXT NOT NULL,
    -- 通道自己的公开参数（Bucket、Region），不含凭据
    config JSONB NOT NULL DEFAULT '{}'::jsonb,
    -- 加密后的凭据（JSON），仅服务端解密使用
    secret_encrypted TEXT NOT NULL DEFAULT '',
    active BOOLEAN NOT NULL DEFAULT TRUE,
    is_default BOOLEAN NOT NULL DEFAULT FALSE,
    last_ok_at TIMESTAMPTZ,
    last_error TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- 同一时间只允许一个默认通道。
CREATE UNIQUE INDEX IF NOT EXISTS ux_oss_providers_default
    ON oss_providers(is_default) WHERE is_default = TRUE;
