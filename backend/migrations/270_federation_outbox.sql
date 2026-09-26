-- 联邦发件箱（POC）：记录需要跨区域同步的事件，当前仅 user.upsert。
-- 由 ent hook 在用户创建的同一事务内写入；外部 pusher 轮询 status='pending' 投递。

CREATE TABLE IF NOT EXISTS federation_outbox_events (
    id              BIGSERIAL PRIMARY KEY,
    aggregate_type  VARCHAR(32) NOT NULL,
    aggregate_id    VARCHAR(64) NOT NULL,
    event_type      VARCHAR(64) NOT NULL,
    payload         TEXT NOT NULL,
    status          VARCHAR(16) NOT NULL DEFAULT 'pending',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_federation_outbox_events_status
    ON federation_outbox_events (status);
