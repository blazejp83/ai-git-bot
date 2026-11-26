-- Outgoing event hooks. secret and headers are encrypted with APP_ENCRYPTION_KEY.
CREATE TABLE IF NOT EXISTS hook_endpoints (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL,
    url TEXT NOT NULL,
    event_types TEXT NOT NULL,          -- comma-separated
    headers TEXT,                       -- encrypted JSON object
    secret TEXT,                        -- encrypted HMAC secret
    bot_id INTEGER,                     -- NULL = every bot
    repo_filter TEXT,                   -- "owner/name" or "owner/*"; NULL = every repository
    skip_tls INTEGER NOT NULL DEFAULT 0,
    enabled INTEGER NOT NULL DEFAULT 1,
    created_at TIMESTAMP NOT NULL,
    updated_at TIMESTAMP NOT NULL
);

CREATE TABLE IF NOT EXISTS hook_deliveries (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    endpoint_id INTEGER NOT NULL REFERENCES hook_endpoints(id) ON DELETE CASCADE,
    event_id TEXT NOT NULL,
    event_type TEXT NOT NULL,
    payload TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'PENDING',  -- PENDING, DELIVERED, FAILED
    attempts INTEGER NOT NULL DEFAULT 0,
    next_attempt_at TIMESTAMP NOT NULL,
    last_status_code INTEGER,
    last_error TEXT,
    created_at TIMESTAMP NOT NULL,
    updated_at TIMESTAMP NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_hook_deliveries_due ON hook_deliveries(status, next_attempt_at);
CREATE INDEX IF NOT EXISTS idx_hook_deliveries_endpoint ON hook_deliveries(endpoint_id, id);
