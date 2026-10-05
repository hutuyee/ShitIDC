-- 023: 邮件通道（对应魔方 public/plugins/mail/ 的 Alimail / Subemail / Btmail 等）。
--
-- 结构与 sms_providers 对齐：一个站点可以配置多条通道，用 is_default 选默认的。
-- 内置 SMTP（system_settings 的历史配置）继续作为兜底：没有启用中的通道时，
-- 发送逻辑回退到 SMTP，因此老部署升级后行为不变。

CREATE TABLE IF NOT EXISTS mail_providers (
    id BIGSERIAL PRIMARY KEY,
    public_id UUID NOT NULL DEFAULT gen_random_uuid() UNIQUE,
    name TEXT NOT NULL,
    -- 通道实现标识：alimail / subemail / btmail / generic
    provider TEXT NOT NULL,
    -- 通道自己的接入参数（发信地址、昵称、请求模板等），不含凭据
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
CREATE UNIQUE INDEX IF NOT EXISTS ux_mail_providers_default
    ON mail_providers(is_default) WHERE is_default = TRUE;
