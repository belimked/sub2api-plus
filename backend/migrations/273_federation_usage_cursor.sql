-- 联邦用量游标（POC）：记录 federation-usage-tailer（海外→大陆用量投递）
-- 已经成功投递到大陆余额的最大 usage_log.id，每个 scope 一行。
-- usage_log 表本身不动——游标状态单独存放，避免碰一张高频写入的热表。

CREATE TABLE IF NOT EXISTS federation_usage_cursors (
    id                  BIGSERIAL PRIMARY KEY,
    scope               VARCHAR(64) NOT NULL,
    last_delivered_id   BIGINT NOT NULL DEFAULT 0,
    attempts            INTEGER NOT NULL DEFAULT 0,
    last_error          TEXT,
    next_retry_at       TIMESTAMPTZ,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_federation_usage_cursors_scope
    ON federation_usage_cursors (scope);
