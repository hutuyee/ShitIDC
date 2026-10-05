-- 025: 人机验证通道（对应魔方 public/plugins/captcha/ 的 GoogleCaptcha / TencentCaptcha）。
--
-- 与 sms_providers / mail_providers 同构：一个站点可配置多条通道，用 is_default
-- 选默认的。没有配置任何通道时，验证码回退内置图形验证码（需要 Redis）。

CREATE TABLE IF NOT EXISTS captcha_providers (
    id BIGSERIAL PRIMARY KEY,
    public_id UUID NOT NULL DEFAULT gen_random_uuid() UNIQUE,
    name TEXT NOT NULL,
    -- 通道实现标识：google_captcha / tencent_captcha
    provider TEXT NOT NULL,
    -- 通道自己的公开参数（站点 SiteKey、CaptchaAppId），不含凭据
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
CREATE UNIQUE INDEX IF NOT EXISTS ux_captcha_providers_default
    ON captcha_providers(is_default) WHERE is_default = TRUE;