-- 011: 计费模型扩展 —— 免费 / 试用 / 一次性商品，以及更多计费周期。
--
-- 对应魔方 shd_products 的 pay_type（free/onetime/recurring/day/hour/ontrial）、
-- 试用相关字段，以及 shd_pricing 的多周期价格列。ShitIDC 的价格本来就是
-- product_prices 一行一个周期，所以“更多周期”只是允许写入更多行，不需要改表；
-- 这里补的是商品级的计费类型与试用配置。

-- 1. 计费类型与试用配置
ALTER TABLE products ADD COLUMN IF NOT EXISTS pay_type TEXT NOT NULL DEFAULT 'recurring';
DO $$ BEGIN
    ALTER TABLE products ADD CONSTRAINT products_pay_type_check
    CHECK (pay_type IN ('recurring','onetime','free','trial'));
EXCEPTION WHEN duplicate_object THEN NULL; END $$;

-- 试用期天数：pay_type='trial' 时生效，开通后 N 天自动回收
ALTER TABLE products ADD COLUMN IF NOT EXISTS trial_days INTEGER NOT NULL DEFAULT 0 CHECK (trial_days >= 0);
-- 试用是否也收钱（0 = 免费试用；>0 = 收一笔试用费）
ALTER TABLE products ADD COLUMN IF NOT EXISTS trial_price_cents BIGINT NOT NULL DEFAULT 0 CHECK (trial_price_cents >= 0);
-- 开通后 N 天自动删除（魔方 auto_terminate_days），0 = 不自动删除
ALTER TABLE products ADD COLUMN IF NOT EXISTS auto_terminate_days INTEGER NOT NULL DEFAULT 0 CHECK (auto_terminate_days >= 0);

-- 2. 计费周期白名单放宽：除月/季/半年/年外，支持小时/天/一次性。
-- 用 DO 块加 EXCEPTION 保证可重复执行（013 还会再把白名单扩到 10 年）。
DO $$ BEGIN
    ALTER TABLE product_prices ADD CONSTRAINT product_prices_billing_cycle_check
    CHECK (billing_cycle IN ('hourly','daily','monthly','quarterly','semiannually','yearly','onetime'));
EXCEPTION WHEN duplicate_object THEN NULL; END $$;

-- 3. 订单侧：标记这笔订单是免费/试用产生的，便于统计与售后
ALTER TABLE orders ADD COLUMN IF NOT EXISTS kind_detail TEXT NOT NULL DEFAULT '';
-- 试用服务的回收时间：Scheduler 用它来终止到期试用
ALTER TABLE services ADD COLUMN IF NOT EXISTS trial_ends_at TIMESTAMPTZ;
CREATE INDEX IF NOT EXISTS idx_services_trial_ends ON services(trial_ends_at) WHERE trial_ends_at IS NOT NULL;
