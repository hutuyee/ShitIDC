-- 022: 自定义（魔方插件导入）供应商与配置项到上游参数的映射。
--
-- 1) config_options.provider_key：商品配置项对应的「上游参数名」。
--    魔方 server 插件读 $params['configoptions'][<key>]，导入插件生成的
--    规格里带着原 key（如 site_max / flow_max）；管理员把商品的配置项
--    挂上同样的 key，开通时就能把买家选择的值传给上游。
--    为空时开通上下文回退用配置项名称作为键。
-- 2) providers 无需改表：custom 供应商的规格存 config JSONB 的 "spec" 键。
ALTER TABLE config_options ADD COLUMN IF NOT EXISTS provider_key TEXT NOT NULL DEFAULT '';
