-- 联邦水位（POC）：记录 users.balance 已经反映到对端 usage_log 的哪个 id 为止。
-- 纯数据记录字段，当前不参与任何请求准入判断。见
-- openspec/changes/federation-balance-sync/proposal.md。

ALTER TABLE users
    ADD COLUMN IF NOT EXISTS federation_usage_watermark_seq BIGINT NOT NULL DEFAULT 0;
