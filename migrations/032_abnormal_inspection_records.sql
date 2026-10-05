-- 032: 异常巡查记录（对齐魔方 CBAP 插件 abnormal_inspection_records）。
--
-- 巡查中发现的异常情况留档：关联用户与产品、异常时 IP、异常事项、处理措施、
-- 处理时间、异常截图（多张，可点击放大）。最新提交人 = 当前后台账号。
-- 列表口径见 docs/parity-roadmap.md §10.18。

CREATE TABLE IF NOT EXISTS abnormal_inspection_records (
    id BIGSERIAL PRIMARY KEY,
    public_id UUID NOT NULL DEFAULT gen_random_uuid() UNIQUE,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    service_id BIGINT NOT NULL REFERENCES services(id) ON DELETE RESTRICT,
    ip TEXT NOT NULL,
    matter TEXT NOT NULL,
    measure TEXT NOT NULL,
    process_time TIMESTAMPTZ NOT NULL,
    -- 截图列表：[{"stored":"<文件名或 oss:key>","name":"原始文件名"}]
    images JSONB NOT NULL DEFAULT '[]'::jsonb,
    admin_id BIGINT REFERENCES users(id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_abnormal_inspection_process ON abnormal_inspection_records(process_time DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_abnormal_inspection_user ON abnormal_inspection_records(user_id);
