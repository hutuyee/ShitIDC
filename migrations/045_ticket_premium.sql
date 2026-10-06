-- 045: 用户工单升级（对齐魔方 CBAP TicketPremium 插件）。
-- 工单部门 / 类型（处理时限）、自定义状态、预设回复、配置、内部备注、操作日志、
-- 接单（领取人）与跟进限制、关联产品、处理完成与用户评分（满意度 / 态度 / 时效）、
-- 催单、回复附件。工单主体沿用 tickets / ticket_messages / ticket_attachments。

-- 1) 部门与类型 -------------------------------------------------------------

CREATE TABLE IF NOT EXISTS ticket_departments (
    id BIGSERIAL PRIMARY KEY,
    name TEXT NOT NULL,
    admin_ids BIGINT[] NOT NULL DEFAULT '{}',
    director_admin_id BIGINT REFERENCES users(id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS ticket_types (
    id BIGSERIAL PRIMARY KEY,
    department_id BIGINT NOT NULL REFERENCES ticket_departments(id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    processing_limit NUMERIC(10,1) NOT NULL DEFAULT 0,
    sort INT NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_ticket_types_department ON ticket_types(department_id, sort, id);

-- 2) 工单状态 ----------------------------------------------------------------

CREATE TABLE IF NOT EXISTS ticket_statuses (
    id BIGSERIAL PRIMARY KEY,
    key TEXT NOT NULL UNIQUE,
    name TEXT NOT NULL,
    color TEXT NOT NULL DEFAULT '#909399',
    finished BOOLEAN NOT NULL DEFAULT FALSE,
    system BOOLEAN NOT NULL DEFAULT FALSE,
    sort INT NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
INSERT INTO ticket_statuses(key,name,color,finished,system,sort) VALUES
  ('open','待处理','#2666FB',FALSE,TRUE,1),
  ('pending','待回复','#F59A23',FALSE,TRUE,2),
  ('closed','已关闭','#909399',TRUE,TRUE,3)
ON CONFLICT (key) DO NOTHING;

-- 3) 预设回复 / 配置 / 备注 / 日志 -------------------------------------------

CREATE TABLE IF NOT EXISTS ticket_prereplies (
    id BIGSERIAL PRIMARY KEY,
    content TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS ticket_config (
    k TEXT PRIMARY KEY,
    v TEXT NOT NULL DEFAULT ''
);
INSERT INTO ticket_config(k,v) VALUES
  ('refresh_time','180'),
  ('ticket_receive_reply','0'),
  ('ticket_follow_reply','0'),
  ('ticket_notice_open','0'),
  ('ticket_notice_description','')
ON CONFLICT (k) DO NOTHING;

CREATE TABLE IF NOT EXISTS ticket_notes (
    id BIGSERIAL PRIMARY KEY,
    ticket_id BIGINT NOT NULL REFERENCES tickets(id) ON DELETE CASCADE,
    admin_id BIGINT REFERENCES users(id) ON DELETE SET NULL,
    content TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_ticket_notes_ticket ON ticket_notes(ticket_id, created_at);

CREATE TABLE IF NOT EXISTS ticket_logs (
    id BIGSERIAL PRIMARY KEY,
    ticket_id BIGINT NOT NULL REFERENCES tickets(id) ON DELETE CASCADE,
    admin_id BIGINT REFERENCES users(id) ON DELETE SET NULL,
    action TEXT NOT NULL DEFAULT '',
    description TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_ticket_logs_ticket ON ticket_logs(ticket_id, created_at DESC);

-- 4) tickets / ticket_messages 扩展 ------------------------------------------

ALTER TABLE tickets ADD COLUMN IF NOT EXISTS number TEXT;
CREATE UNIQUE INDEX IF NOT EXISTS idx_tickets_number ON tickets(number) WHERE number IS NOT NULL;
ALTER TABLE tickets ADD COLUMN IF NOT EXISTS department_id BIGINT REFERENCES ticket_departments(id) ON DELETE SET NULL;
ALTER TABLE tickets ADD COLUMN IF NOT EXISTS ticket_type_id BIGINT REFERENCES ticket_types(id) ON DELETE SET NULL;
ALTER TABLE tickets ADD COLUMN IF NOT EXISTS host_ids BIGINT[] NOT NULL DEFAULT '{}';
ALTER TABLE tickets ADD COLUMN IF NOT EXISTS attachment TEXT[] NOT NULL DEFAULT '{}';
ALTER TABLE tickets ADD COLUMN IF NOT EXISTS admin_id BIGINT REFERENCES users(id) ON DELETE SET NULL;
ALTER TABLE tickets ADD COLUMN IF NOT EXISTS last_reply_admin_id BIGINT REFERENCES users(id) ON DELETE SET NULL;
ALTER TABLE tickets ADD COLUMN IF NOT EXISTS post_admin_id BIGINT REFERENCES users(id) ON DELETE SET NULL;
ALTER TABLE tickets ADD COLUMN IF NOT EXISTS finished BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE tickets ADD COLUMN IF NOT EXISTS finish_time TIMESTAMPTZ;
ALTER TABLE tickets ADD COLUMN IF NOT EXISTS is_score BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE tickets ADD COLUMN IF NOT EXISTS satisfaction NUMERIC(3,2);
ALTER TABLE tickets ADD COLUMN IF NOT EXISTS attitude NUMERIC(3,2);
ALTER TABLE tickets ADD COLUMN IF NOT EXISTS processing_time NUMERIC(3,2);
ALTER TABLE tickets ADD COLUMN IF NOT EXISTS score_time TIMESTAMPTZ;
ALTER TABLE tickets ADD COLUMN IF NOT EXISTS last_urge_time TIMESTAMPTZ;
ALTER TABLE tickets ADD COLUMN IF NOT EXISTS urge_count INT NOT NULL DEFAULT 0;
ALTER TABLE tickets DROP CONSTRAINT IF EXISTS tickets_status_check;

CREATE INDEX IF NOT EXISTS idx_tickets_department ON tickets(department_id);
CREATE INDEX IF NOT EXISTS idx_tickets_admin ON tickets(admin_id, created_at DESC);

ALTER TABLE ticket_messages ADD COLUMN IF NOT EXISTS attachment TEXT[] NOT NULL DEFAULT '{}';
