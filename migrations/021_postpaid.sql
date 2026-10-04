-- 021: 后付费（对应魔方 pay_method = postpaid）。
--
-- 语义：授信客户先开通、后付款，到期未付则暂停服务。
-- 「授信」是这个功能的核心风险点，所以额度是**显式授予**的：
--   * postpaid_enabled 默认 FALSE —— 不能靠注册就拿到赊账能力
--   * credit_limit_cents 是总额度，未付订单会占用它
--   * 未付占用是「已生成但未支付的 postpaid 订单金额之和」，实时算，不另存冗余字段，
--     避免出现「额度字段和实际占用不一致」这种最难查的账目问题
--
-- 与普通订单的区别：
--   * orders.pay_method 记录 'prepaid' / 'postpaid'
--   * postpaid 订单创建后直接进入开通流程，不等付款
--   * 发票 due_at 就是账期到期时间；过期未付由调度器暂停服务

-- 1. 用户授信。
ALTER TABLE users ADD COLUMN IF NOT EXISTS postpaid_enabled BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE users ADD COLUMN IF NOT EXISTS credit_limit_cents BIGINT NOT NULL DEFAULT 0
    CHECK (credit_limit_cents >= 0);
-- 账期天数：从开单到必须付款的时间。
ALTER TABLE users ADD COLUMN IF NOT EXISTS credit_days INTEGER NOT NULL DEFAULT 30
    CHECK (credit_days >= 0 AND credit_days <= 365);

-- 2. 订单记录支付方式，便于区分与统计。
ALTER TABLE orders ADD COLUMN IF NOT EXISTS pay_method TEXT NOT NULL DEFAULT 'prepaid';
DO $$ BEGIN
    ALTER TABLE orders ADD CONSTRAINT orders_pay_method_check
    CHECK (pay_method IN ('prepaid','postpaid'));
EXCEPTION WHEN duplicate_object THEN NULL; END $$;
CREATE INDEX IF NOT EXISTS idx_orders_postpaid_unpaid
    ON orders(user_id, status) WHERE pay_method = 'postpaid' AND status = 'unpaid';

-- 3. 服务记录是否是后付费开通，以及因此产生的欠款订单。
ALTER TABLE services ADD COLUMN IF NOT EXISTS pay_method TEXT NOT NULL DEFAULT 'prepaid';
ALTER TABLE services ADD COLUMN IF NOT EXISTS postpaid_order_id BIGINT REFERENCES orders(id) ON DELETE SET NULL;
-- 因为后付费欠款而被暂停的服务，标记出来以便付款后自动恢复。
ALTER TABLE services ADD COLUMN IF NOT EXISTS suspended_for_nonpayment BOOLEAN NOT NULL DEFAULT FALSE;
CREATE INDEX IF NOT EXISTS idx_services_postpaid_overdue
    ON services(suspended_for_nonpayment) WHERE suspended_for_nonpayment = TRUE;

-- 4. 授信变更流水：额度调整必须留痕，否则出了坏账无法复盘。
CREATE TABLE IF NOT EXISTS credit_events (
    id BIGSERIAL PRIMARY KEY,
    public_id UUID NOT NULL DEFAULT gen_random_uuid() UNIQUE,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    -- grant 提额 / revoke 降额 / enable 开通 / disable 停用 / suspend 因欠款暂停
    event TEXT NOT NULL,
    before_limit_cents BIGINT NOT NULL DEFAULT 0,
    after_limit_cents BIGINT NOT NULL DEFAULT 0,
    note TEXT NOT NULL DEFAULT '',
    actor_user_id BIGINT REFERENCES users(id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_credit_events_user ON credit_events(user_id, created_at DESC);
