-- 026: 实名认证支持扫码类通道（支付宝芝麻信用 / 微信人脸核身等）。
--
-- 扫码通道无法同步出结果：提交时先落一条 pending，把通道凭证（certify_id /
-- BizToken）与二维码地址存下来，前端轮询 /profile/certification/poll 再定终态。
-- 两者都不含用户明文（姓名/证件号在上游侧），只存引用；不对外返回。

ALTER TABLE certifications ADD COLUMN IF NOT EXISTS provider_ref TEXT NOT NULL DEFAULT '';
ALTER TABLE certifications ADD COLUMN IF NOT EXISTS provider_url TEXT NOT NULL DEFAULT '';
