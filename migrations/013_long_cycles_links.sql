-- 013: 长周期（2~10 年）与配置项条件联动（对应魔方 biennially..tenly 与
-- product_config_options_links）。

-- 1. 放宽计费周期白名单：补上 2 年到 10 年的长周期。
-- 必须先删掉 011 建立的旧约束，否则新约束不会生效。
ALTER TABLE product_prices DROP CONSTRAINT IF EXISTS product_prices_billing_cycle_check;
DO $$ BEGIN
    ALTER TABLE product_prices ADD CONSTRAINT product_prices_billing_cycle_check
    CHECK (billing_cycle IN ('hourly','daily','monthly','quarterly','semiannually','yearly',
                             'biennially','triennially','fourly','fively','sixly','sevenly',
                             'eightly','ninely','tenly','onetime'));
EXCEPTION WHEN duplicate_object THEN NULL; END $$;

-- 2. 配置项条件联动：选了某个候选项，才显示/必填另一批配置项。
--    与魔方一致地存成“条件组合 → 结果配置项”的规则表。
CREATE TABLE IF NOT EXISTS config_option_links (
    id BIGSERIAL PRIMARY KEY,
    public_id UUID NOT NULL DEFAULT gen_random_uuid() UNIQUE,
    -- 触发条件：source 配置项的某个（或多个）候选项
    source_option_id BIGINT NOT NULL REFERENCES config_options(id) ON DELETE CASCADE,
    source_value_id BIGINT NOT NULL REFERENCES config_option_values(id) ON DELETE CASCADE,
    -- 被影响的配置项
    target_option_id BIGINT NOT NULL REFERENCES config_options(id) ON DELETE CASCADE,
    -- 命中条件时：显示，并且（可选）变为必填
    visible BOOLEAN NOT NULL DEFAULT TRUE,
    required BOOLEAN NOT NULL DEFAULT FALSE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (source_value_id, target_option_id)
);
CREATE INDEX IF NOT EXISTS idx_config_option_links_target ON config_option_links(target_option_id);
CREATE INDEX IF NOT EXISTS idx_config_option_links_source ON config_option_links(source_option_id);
