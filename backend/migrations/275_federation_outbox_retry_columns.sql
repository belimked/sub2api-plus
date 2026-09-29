-- 联邦发件箱重试退避列：pusher 用来记录尝试次数/最近错误/下次重试时间。
-- 272_federation_outbox.sql 已经在部署环境应用过，不能原地改（迁移不可变），
-- 这三列和对应索引改成独立的追加迁移。

ALTER TABLE federation_outbox_events
    ADD COLUMN IF NOT EXISTS attempts      INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS last_error    TEXT,
    ADD COLUMN IF NOT EXISTS next_retry_at TIMESTAMPTZ;

CREATE INDEX IF NOT EXISTS idx_federation_outbox_events_status_next_retry_at
    ON federation_outbox_events (status, next_retry_at);
