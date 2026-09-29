CREATE TABLE IF NOT EXISTS claude_code_account_identities (
    account_id BIGINT PRIMARY KEY REFERENCES accounts(id) ON DELETE CASCADE,
    device_id TEXT NOT NULL CHECK (device_id ~ '^[0-9a-f]{64}$'),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

COMMENT ON TABLE claude_code_account_identities IS
    'Private stable Claude Code device ID per Anthropic OAuth or setup-token account.';
