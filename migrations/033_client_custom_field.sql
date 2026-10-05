-- 033: 客户自定义字段（对齐魔方 CBAP 插件 client_custom_field）。
--
-- 后台自定义「用户详情」可输入的信息（文本/下拉/链接/密码/勾选框/文本区/下拉文本框），
-- 可设必填、订购前必填、管理员可见、注册时显示、显示状态、排序与正则校验；
-- 用户可在个人中心填写，注册时可一并提交，管理员在用户详情查看。
-- password 类型不回显明文（读取接口只返回「是否已设置」）。

CREATE TABLE IF NOT EXISTS client_custom_fields (
    id BIGSERIAL PRIMARY KEY,
    public_id UUID NOT NULL DEFAULT gen_random_uuid() UNIQUE,
    name TEXT NOT NULL,
    type TEXT NOT NULL DEFAULT 'text' CHECK (type IN ('text','dropdown','link','password','tickbox','textarea','dropdown_text')),
    options TEXT NOT NULL DEFAULT '',
    description TEXT NOT NULL DEFAULT '',
    regexpr TEXT NOT NULL DEFAULT '',
    admin_only BOOLEAN NOT NULL DEFAULT TRUE,
    required BOOLEAN NOT NULL DEFAULT FALSE,
    before_settle BOOLEAN NOT NULL DEFAULT FALSE,
    show_register BOOLEAN NOT NULL DEFAULT TRUE,
    status BOOLEAN NOT NULL DEFAULT TRUE,
    sort_weight BIGINT NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS client_custom_field_values (
    id BIGSERIAL PRIMARY KEY,
    field_id BIGINT NOT NULL REFERENCES client_custom_fields(id) ON DELETE CASCADE,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    value TEXT NOT NULL DEFAULT '',
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (field_id, user_id)
);
CREATE INDEX IF NOT EXISTS idx_client_custom_field_values_user ON client_custom_field_values(user_id);
