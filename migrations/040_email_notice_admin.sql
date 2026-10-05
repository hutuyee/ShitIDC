-- 值邮件通知管理员（对齐魔方 CBAP EmailNoticeAdmin 插件）。
-- 规则存于 system_settings(key=email_notice_admin)，这里登记后台权限点。

INSERT INTO permissions(name, description) VALUES
('email_notice.manage','管理值邮件通知（动作 / 邮件接口 / 邮件模板 / 通知人员 / 启用）')
ON CONFLICT(name) DO NOTHING;

INSERT INTO role_permissions(role_id, permission_id)
SELECT r.id,p.id FROM roles r, permissions p
WHERE r.name='admin' AND p.name='email_notice.manage'
ON CONFLICT DO NOTHING;
