-- 028_export_excel.sql —— 对齐魔方附属插件 export_excel（数据导出至 Excel）。
--
-- 参考插件用 shd_export_plugin 保存「自定义名称 + 导出列表 + 参数字段」，
-- 后台选择列表与字段、按时间区间导出 Excel；插件自带两个列表：
--   billPay     账单列表（已支付）
--   achievement 我的业绩（按业务经理口径；ShitIDC 无业务经理，按推广人佣金口径）
CREATE TABLE IF NOT EXISTS export_configs (
    id BIGSERIAL PRIMARY KEY,
    custom_name TEXT NOT NULL,
    dataset TEXT NOT NULL,
    columns TEXT[] NOT NULL DEFAULT '{}',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
INSERT INTO export_configs(custom_name, dataset, columns)
SELECT '已支付账单', 'bill_pay',
       ARRAY['bill_num','g_name','pd_name','croom','cip','paytype','amount','stream','balance','mount_at']
WHERE NOT EXISTS (SELECT 1 FROM export_configs WHERE dataset='bill_pay');
INSERT INTO export_configs(custom_name, dataset, columns)
SELECT '推广业绩', 'achievement',
       ARRAY['referrer','referee','bill_num','amount','commission','mount_at']
WHERE NOT EXISTS (SELECT 1 FROM export_configs WHERE dataset='achievement');
