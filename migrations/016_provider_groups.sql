-- 016: 接口分组与容量分配（对应魔方 shd_server_groups「平均分配 / 满一个算一个」）。
--
-- 魔方允许把一个商品绑定到「接口分组」而不是单个接口，开通时由核心按策略挑一个
-- 分组内的接口。这样加机器只要往分组里加接口，商品不用改。
--
-- 本实现的策略：
--   least_loaded  选当前承载服务数最少的（默认，最接近「平均分配」）
--   fill_first    选第一个装得下的（凑满一个再下一个）
--   round_robin   按已开通数量取模轮转
-- 每个接口还可以配 max_services 容量上限（0 = 不限）。

CREATE TABLE IF NOT EXISTS provider_groups (
    id BIGSERIAL PRIMARY KEY,
    public_id UUID NOT NULL DEFAULT gen_random_uuid() UNIQUE,
    name TEXT NOT NULL UNIQUE,
    strategy TEXT NOT NULL DEFAULT 'least_loaded'
        CHECK (strategy IN ('least_loaded','fill_first','round_robin')),
    description TEXT NOT NULL DEFAULT '',
    active BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- 一个接口可以属于多个分组。
CREATE TABLE IF NOT EXISTS provider_group_members (
    id BIGSERIAL PRIMARY KEY,
    group_id BIGINT NOT NULL REFERENCES provider_groups(id) ON DELETE CASCADE,
    provider_id BIGINT NOT NULL REFERENCES providers(id) ON DELETE CASCADE,
    -- 0 = 不限容量
    max_services INTEGER NOT NULL DEFAULT 0 CHECK (max_services >= 0),
    -- 组内权重：轮转/填充时的优先顺序，越大越优先
    weight INTEGER NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (group_id, provider_id)
);
CREATE INDEX IF NOT EXISTS idx_provider_group_members_group ON provider_group_members(group_id);

-- 商品可以绑定到分组；绑定了分组就优先按分组挑接口。
ALTER TABLE products ADD COLUMN IF NOT EXISTS provider_group_id BIGINT REFERENCES provider_groups(id) ON DELETE SET NULL;
CREATE INDEX IF NOT EXISTS idx_products_provider_group ON products(provider_group_id) WHERE provider_group_id IS NOT NULL;

-- 服务上记录最终选中的接口，便于排查「这台机器到底开在哪」。
ALTER TABLE services ADD COLUMN IF NOT EXISTS provider_group_id BIGINT REFERENCES provider_groups(id) ON DELETE SET NULL;
