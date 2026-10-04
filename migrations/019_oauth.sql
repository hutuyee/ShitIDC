-- 019: 第三方登录（对应魔方 public/plugins/oauth/）。
--
-- 模型：一个用户可以绑定多个第三方身份；一个第三方身份只能属于一个用户。
-- 自动注册的账号没有密码，靠 oauth 登录；用户也可以在后台设置密码后改用密码登录。

-- 1. 第三方登录通道配置。
CREATE TABLE IF NOT EXISTS oauth_providers (
    id BIGSERIAL PRIMARY KEY,
    public_id UUID NOT NULL DEFAULT gen_random_uuid() UNIQUE,
    name TEXT NOT NULL,
    -- 通道实现标识：wechat / qq / weibo / github / oidc
    provider TEXT NOT NULL,
    -- ClientID 一般不敏感，放 config；ClientSecret 加密后放 secret_encrypted
    config JSONB NOT NULL DEFAULT '{}'::jsonb,
    secret_encrypted TEXT NOT NULL DEFAULT '',
    -- 是否允许「没有账号时自动注册」
    allow_register BOOLEAN NOT NULL DEFAULT TRUE,
    active BOOLEAN NOT NULL DEFAULT TRUE,
    is_default BOOLEAN NOT NULL DEFAULT FALSE,
    last_ok_at TIMESTAMPTZ,
    last_error TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (provider)
);

-- 2. 第三方身份绑定。
CREATE TABLE IF NOT EXISTS oauth_identities (
    id BIGSERIAL PRIMARY KEY,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    provider TEXT NOT NULL,
    -- 该平台内的用户唯一标识（微信是 openid，QQ 是 openid，GitHub 是 id）
    subject TEXT NOT NULL,
    -- 跨应用统一标识（微信 unionid / QQ unionid）：同一个人在不同应用里能合并
    union_id TEXT NOT NULL DEFAULT '',
    nickname TEXT NOT NULL DEFAULT '',
    avatar_url TEXT NOT NULL DEFAULT '',
    email TEXT NOT NULL DEFAULT '',
    raw JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- 同一个平台的同一个 subject 只能绑一个账号
    UNIQUE (provider, subject)
);
CREATE INDEX IF NOT EXISTS idx_oauth_identities_user ON oauth_identities(user_id);
CREATE INDEX IF NOT EXISTS idx_oauth_identities_union ON oauth_identities(provider, union_id) WHERE union_id <> '';

-- 3. 授权时的 state（防 CSRF 与重放）。
--    一次性使用，5 分钟过期。
CREATE TABLE IF NOT EXISTS oauth_states (
    state TEXT PRIMARY KEY,
    provider TEXT NOT NULL,
    -- 登录后跳回哪里（站内相对路径，防止开放重定向）
    redirect_to TEXT NOT NULL DEFAULT '',
    -- 已登录用户发起的「绑定」操作会带 user_id；未登录是登录/注册
    user_id BIGINT REFERENCES users(id) ON DELETE CASCADE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_oauth_states_created ON oauth_states(created_at);
