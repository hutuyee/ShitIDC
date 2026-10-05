-- 024: 企业微信服务商应用的 suite_ticket 缓存。
--
-- 企业微信会每 10 分钟把 suite_ticket 推送到服务商后台配置的「指令回调 URL」，
-- 换 suite_access_token 必须带上最近一次 ticket。这里按 suite_id 存最新值。

CREATE TABLE IF NOT EXISTS oauth_suite_tickets (
    suite_id TEXT PRIMARY KEY,
    ticket TEXT NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
