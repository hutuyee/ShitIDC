-- 039: 发票申请补充快递单照片字段（对齐魔方 IdcsmartInvoice 插件纸质发票发出时的
-- 「快递单照片」上传；本机相对路径或 OSS 地址以 oss: 前缀记录）。
ALTER TABLE invoice_requests ADD COLUMN IF NOT EXISTS parcel_image TEXT NOT NULL DEFAULT '';
