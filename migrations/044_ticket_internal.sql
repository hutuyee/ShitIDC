-- 044: 内部工单（对齐魔方 CBAP TicketInternalPremium 插件）。
-- 内部工单是管理端内部流转的工单：管理员发起，指定部门与工单类型；部门人员可接单、
-- 回复、转单、处理完成与关闭；工单按部门类型的处理时限计算超时与「即将超时」，
-- 支持日志、内部备注、预设回复、评分（发起人评分 / 主管评分）与统计排名、定时工单。

CREATE TABLE IF NOT EXISTS ticket_internal_departments (
    id BIGSERIAL PRIMARY KEY,
    name TEXT NOT NULL,
    admin_ids BIGINT[] NOT NULL DEFAULT '{}',
    director_admin_id BIGINT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS ticket_internal_types (
    id BIGSERIAL PRIMARY KEY,
    department_id BIGINT NOT NULL REFERENCES ticket_internal_departments(id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    processing_limit INTEGER NOT NULL DEFAULT 24,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_ticket_internal_types_department ON ticket_internal_types(department_id);

CREATE TABLE IF NOT EXISTS ticket_internal_statuses (
    id BIGSERIAL PRIMARY KEY,
    key TEXT UNIQUE,
    name TEXT NOT NULL,
    color TEXT NOT NULL DEFAULT '#0052D9',
    finished BOOLEAN NOT NULL DEFAULT FALSE,
    system BOOLEAN NOT NULL DEFAULT FALSE,
    sort INTEGER NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

INSERT INTO ticket_internal_statuses(key,name,color,finished,system,sort) VALUES
('wait','待接单','#E37318',FALSE,TRUE,1),
('waiting','待回复','#0052D9',FALSE,TRUE,2),
('replied','已回复','#2BA471',FALSE,TRUE,3),
('closed','已关闭','#909399',TRUE,TRUE,4)
ON CONFLICT (key) DO NOTHING;

CREATE TABLE IF NOT EXISTS ticket_internal_prereplies (
    id BIGSERIAL PRIMARY KEY,
    content TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS ticket_internal_config (
    k TEXT PRIMARY KEY,
    v TEXT NOT NULL DEFAULT ''
);

INSERT INTO ticket_internal_config(k,v) VALUES
('order_button','0'),
('follow_limit','0'),
('will_timeout_notice','0'),
('refresh_time','180')
ON CONFLICT (k) DO NOTHING;

CREATE TABLE IF NOT EXISTS ticket_internal_tickets (
    id BIGSERIAL PRIMARY KEY,
    title TEXT NOT NULL,
    department_id BIGINT NOT NULL REFERENCES ticket_internal_departments(id) ON DELETE RESTRICT,
    type_id BIGINT NOT NULL REFERENCES ticket_internal_types(id) ON DELETE RESTRICT,
    status_id BIGINT NOT NULL REFERENCES ticket_internal_statuses(id) ON DELETE RESTRICT,
    creator_id BIGINT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    client_id BIGINT REFERENCES users(id) ON DELETE SET NULL,
    host_ids BIGINT[] NOT NULL DEFAULT '{}',
    source_ticket_id BIGINT REFERENCES tickets(id) ON DELETE SET NULL,
    priority TEXT NOT NULL DEFAULT 'normal',
    content TEXT NOT NULL DEFAULT '',
    notes TEXT NOT NULL DEFAULT '',
    attachments JSONB NOT NULL DEFAULT '[]'::jsonb,
    assignee_id BIGINT REFERENCES users(id) ON DELETE SET NULL,
    acceptor_id BIGINT REFERENCES users(id) ON DELETE SET NULL,
    last_reply_at TIMESTAMPTZ,
    last_reply_admin_id BIGINT,
    finish_at TIMESTAMPTZ,
    reminded_at TIMESTAMPTZ,
    satisfaction NUMERIC(3,1),
    attitude NUMERIC(3,1),
    processing_score NUMERIC(3,1),
    director_satisfaction NUMERIC(3,1),
    director_attitude NUMERIC(3,1),
    director_processing_score NUMERIC(3,1),
    cron_id BIGINT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_ticket_internal_tickets_status ON ticket_internal_tickets(status_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_ticket_internal_tickets_department ON ticket_internal_tickets(department_id, created_at DESC);

CREATE TABLE IF NOT EXISTS ticket_internal_replies (
    id BIGSERIAL PRIMARY KEY,
    ticket_id BIGINT NOT NULL REFERENCES ticket_internal_tickets(id) ON DELETE CASCADE,
    admin_id BIGINT REFERENCES users(id) ON DELETE SET NULL,
    content TEXT NOT NULL,
    attachments JSONB NOT NULL DEFAULT '[]'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_ticket_internal_replies_ticket ON ticket_internal_replies(ticket_id, created_at);

CREATE TABLE IF NOT EXISTS ticket_internal_notes (
    id BIGSERIAL PRIMARY KEY,
    ticket_id BIGINT NOT NULL REFERENCES ticket_internal_tickets(id) ON DELETE CASCADE,
    admin_id BIGINT REFERENCES users(id) ON DELETE SET NULL,
    content TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_ticket_internal_notes_ticket ON ticket_internal_notes(ticket_id, created_at);

CREATE TABLE IF NOT EXISTS ticket_internal_logs (
    id BIGSERIAL PRIMARY KEY,
    ticket_id BIGINT NOT NULL REFERENCES ticket_internal_tickets(id) ON DELETE CASCADE,
    admin_id BIGINT REFERENCES users(id) ON DELETE SET NULL,
    description TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_ticket_internal_logs_ticket ON ticket_internal_logs(ticket_id, created_at);

CREATE TABLE IF NOT EXISTS ticket_internal_cron_jobs (
    id BIGSERIAL PRIMARY KEY,
    title TEXT NOT NULL,
    content TEXT NOT NULL DEFAULT '',
    cycle_period INTEGER NOT NULL DEFAULT 0,
    unit TEXT NOT NULL DEFAULT 'day',
    start_at TIMESTAMPTZ NOT NULL,
    end_at TIMESTAMPTZ,
    trigger_time TEXT NOT NULL DEFAULT '09:00',
    department_id BIGINT NOT NULL REFERENCES ticket_internal_departments(id) ON DELETE RESTRICT,
    type_id BIGINT NOT NULL REFERENCES ticket_internal_types(id) ON DELETE RESTRICT,
    admin_id BIGINT REFERENCES users(id) ON DELETE SET NULL,
    creator_id BIGINT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    status INTEGER NOT NULL DEFAULT 1,
    next_run_at TIMESTAMPTZ,
    last_run_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_ticket_internal_cron_due ON ticket_internal_cron_jobs(status, next_run_at);

INSERT INTO permissions(name, description) VALUES
('ticket_internal.manage','内部工单（内部工单 / 工单配置 / 定时工单 / 工单统计）')
ON CONFLICT(name) DO NOTHING;

INSERT INTO role_permissions(role_id, permission_id)
SELECT r.id,p.id FROM roles r, permissions p
WHERE r.name='admin' AND p.name='ticket_internal.manage'
ON CONFLICT DO NOTHING;
