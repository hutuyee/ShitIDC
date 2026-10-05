-- 客户关怀（对齐魔方 CBAP ClientCare 插件）。
-- 推送任务（站内信 / 邮件）+ 筛选条件（push_object JSONB）+ 用户站内信收件箱。

CREATE TABLE IF NOT EXISTS client_care_jobs (
    id BIGSERIAL PRIMARY KEY,
    public_id UUID NOT NULL DEFAULT gen_random_uuid() UNIQUE,
    title TEXT NOT NULL,
    type INTEGER NOT NULL DEFAULT 1,           -- 1 站内信 / 2 邮件（原插件为 短信/邮件）
    content TEXT NOT NULL DEFAULT '',          -- 站内信 content 或邮件 message（HTML）
    subject TEXT NOT NULL DEFAULT '',          -- 邮件标题
    email_name TEXT NOT NULL DEFAULT '',       -- 邮件通道（mail_providers.public_id，空=默认通道）
    sms_name TEXT NOT NULL DEFAULT '',         -- 保留字段：本站短信通道只支持验证码模板，暂不投递
    sms_template_id BIGINT NOT NULL DEFAULT 0,
    push_start_time TIMESTAMPTZ NOT NULL DEFAULT now(),
    push_end_time TIMESTAMPTZ NOT NULL DEFAULT now(),
    send_cycle TEXT NOT NULL DEFAULT 'onetime' CHECK (send_cycle IN ('onetime','day','week','month')),
    week_day INTEGER NOT NULL DEFAULT 1,       -- 0=周日 … 6=周六（与 Go time.Weekday 一致）
    month_day INTEGER NOT NULL DEFAULT 1,
    hour INTEGER NOT NULL DEFAULT 0,
    minute INTEGER NOT NULL DEFAULT 0,
    repeat_send BOOLEAN NOT NULL DEFAULT FALSE,
    push_object JSONB NOT NULL DEFAULT '{}'::jsonb,
    status TEXT NOT NULL DEFAULT 'wait' CHECK (status IN ('wait','exec','suspended','finished','expired')),
    next_run_at TIMESTAMPTZ,
    last_run_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_client_care_jobs_due ON client_care_jobs(status, next_run_at);

CREATE TABLE IF NOT EXISTS client_care_mails (
    id BIGSERIAL PRIMARY KEY,
    public_id UUID NOT NULL DEFAULT gen_random_uuid() UNIQUE,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    job_id BIGINT REFERENCES client_care_jobs(id) ON DELETE SET NULL,
    title TEXT NOT NULL DEFAULT '',
    content TEXT NOT NULL DEFAULT '',
    read_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_client_care_mails_user ON client_care_mails(user_id, id DESC);
CREATE INDEX IF NOT EXISTS idx_client_care_mails_job_user ON client_care_mails(job_id, user_id);

INSERT INTO permissions(name, description) VALUES
('client_care.manage','管理客户关怀（推送任务 / 推送名单 / 站内信）')
ON CONFLICT(name) DO NOTHING;

INSERT INTO role_permissions(role_id, permission_id)
SELECT r.id,p.id FROM roles r, permissions p
WHERE r.name='admin' AND p.name='client_care.manage'
ON CONFLICT DO NOTHING;
