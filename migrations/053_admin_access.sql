-- 后台面板访问权限：把「能进后台」与客户侧权限彻底分开。
-- 背景：后台路由此前逐个用权限守卫，其中商品列表、订单列表、超量计费结算/明细
-- 误用了客户角色同样拥有的权限（product.read / order.read / service.read / service.operate），
-- 客户令牌可直接调用后台接口。新增 admin.access，由路由组统一要求。
-- 存量库需手动执行本文件（新库由 docker-entrypoint-initdb.d 自动执行）。

INSERT INTO permissions(name, description) VALUES
('admin.access','访问后台管理面板（客服/财务/管理员）')
ON CONFLICT(name) DO NOTHING;

INSERT INTO role_permissions(role_id, permission_id)
SELECT r.id,p.id FROM roles r, permissions p
WHERE r.name IN ('support','finance','admin') AND p.name='admin.access'
ON CONFLICT DO NOTHING;
