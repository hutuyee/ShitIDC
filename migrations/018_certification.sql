-- 018: 实名认证（对应魔方 public/plugins/certification/）。
--
-- 设计要点：
--   * 身份证号是敏感信息：只存「掩码后的展示串」与「哈希」，绝不存明文。
--     哈希用于防止同一证件重复注册多个账号。
--   * 姓名同样是敏感信息：只存掩码（张*三）与哈希。
--   * 认证可以是「通道自动核验」也可以是「人工审核」，两种结果都写同一张表。

-- 1. 实名通道配置（一个站点可配多个，is_default 选默认）。
CREATE TABLE IF NOT EXISTS certification_providers (
    id BIGSERIAL PRIMARY KEY,
    public_id UUID NOT NULL DEFAULT gen_random_uuid() UNIQUE,
    name TEXT NOT NULL,
    -- 通道实现标识：aliyun_idcard / manual
    provider TEXT NOT NULL,
    config JSONB NOT NULL DEFAULT '{}'::jsonb,
    secret_encrypted TEXT NOT NULL DEFAULT '',
    active BOOLEAN NOT NULL DEFAULT TRUE,
    is_default BOOLEAN NOT NULL DEFAULT FALSE,
    last_ok_at TIMESTAMPTZ,
    last_error TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX IF NOT EXISTS ux_certification_providers_default
    ON certification_providers(is_default) WHERE is_default = TRUE;

-- 2. 认证记录。一个用户同时只保留一条当前记录（重新提交会覆盖）。
CREATE TABLE IF NOT EXISTS certifications (
    id BIGSERIAL PRIMARY KEY,
    public_id UUID NOT NULL DEFAULT gen_random_uuid() UNIQUE,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE UNIQUE,
    -- 状态：pending 待审核 / approved 已通过 / rejected 已驳回
    status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','approved','rejected')),
    -- 真实姓名的掩码（张*三），用于展示
    real_name_masked TEXT NOT NULL DEFAULT '',
    -- 证件类型：idcard 身份证 / passport 护照 / license 驾照
    id_type TEXT NOT NULL DEFAULT 'idcard' CHECK (id_type IN ('idcard','passport','license')),
    -- 证件号的掩码（110***********1234）
    id_number_masked TEXT NOT NULL DEFAULT '',
    -- 姓名与证件号的哈希（HMAC），用于防重复绑定；不可逆
    name_hash TEXT NOT NULL DEFAULT '',
    id_number_hash TEXT NOT NULL DEFAULT '',
    -- 解析出的性别与出生日期（身份证前 18 位可解），用于风控与统计
    gender TEXT NOT NULL DEFAULT '',
    birth_date DATE,
    -- 核验方式与结果
    provider TEXT NOT NULL DEFAULT '',
    verified_by TEXT NOT NULL DEFAULT '',
    reject_reason TEXT NOT NULL DEFAULT '',
    submitted_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    reviewed_at TIMESTAMPTZ,
    reviewed_by BIGINT REFERENCES users(id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_certifications_status ON certifications(status);
-- 同一个证件号（哈希）只能绑定一个账号。
CREATE UNIQUE INDEX IF NOT EXISTS ux_certifications_id_number
    ON certifications(id_number_hash) WHERE id_number_hash <> '' AND status = 'approved';

-- 3. 是否需要实名才能下单，做成站点设置（按业务开关）。
INSERT INTO system_settings(key, value)
VALUES ('certification_required', 'false'::jsonb)
ON CONFLICT (key) DO NOTHING;
