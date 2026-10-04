INSERT INTO permissions(name, description) VALUES
('user.read','Read users'),
('product.read','Read products'),
('product.write','Manage products'),
('order.read','Read own orders'),
('invoice.read','Read own invoices'),
('wallet.read','Read own wallet'),
('wallet.adjust','Adjust user wallet'),
('service.read','Read own services'),
('service.operate','Operate services'),
('ticket.read','Read tickets'),
('ticket.write','Create/reply tickets'),
('api_token.manage','Manage own API tokens'),
('provider.manage','Manage providers'),
('security.audit.read','Read audit logs')
ON CONFLICT(name) DO NOTHING;

INSERT INTO roles(name, description) VALUES
('customer','Normal client account'),
('support','Support staff'),
('finance','Finance staff'),
('admin','System administrator')
ON CONFLICT(name) DO NOTHING;

INSERT INTO role_permissions(role_id, permission_id)
SELECT r.id,p.id FROM roles r CROSS JOIN permissions p
WHERE r.name='customer' AND p.name IN ('product.read','order.read','invoice.read','wallet.read','service.read','service.operate','ticket.read','ticket.write','api_token.manage')
ON CONFLICT DO NOTHING;

INSERT INTO role_permissions(role_id, permission_id)
SELECT r.id,p.id FROM roles r CROSS JOIN permissions p
WHERE r.name='support' AND p.name IN ('user.read','product.read','order.read','invoice.read','service.read','service.operate','ticket.read','ticket.write')
ON CONFLICT DO NOTHING;

INSERT INTO role_permissions(role_id, permission_id)
SELECT r.id,p.id FROM roles r CROSS JOIN permissions p
WHERE r.name='finance' AND p.name IN ('user.read','product.read','order.read','invoice.read','wallet.read','wallet.adjust','security.audit.read')
ON CONFLICT DO NOTHING;

INSERT INTO role_permissions(role_id, permission_id)
SELECT r.id,p.id FROM roles r CROSS JOIN permissions p
WHERE r.name='admin'
ON CONFLICT DO NOTHING;
