-- One row per AI request. Integration and bot are stored by name (no foreign
-- key) so deleting them keeps the history. input_tokens excludes cached tokens.
CREATE TABLE IF NOT EXISTS ai_usage_log (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    recorded_at TIMESTAMP NOT NULL,
    integration TEXT NOT NULL,
    provider TEXT,
    model TEXT,
    bot TEXT,
    session TEXT,
    round INTEGER NOT NULL DEFAULT 0,
    kind TEXT,
    input_tokens INTEGER NOT NULL DEFAULT 0,
    output_tokens INTEGER NOT NULL DEFAULT 0,
    cache_read_tokens INTEGER NOT NULL DEFAULT 0,
    cache_write_tokens INTEGER NOT NULL DEFAULT 0,
    -- Estimated at list prices when recorded; NULL for models without a known price.
    est_cost_usd REAL
);
CREATE INDEX IF NOT EXISTS idx_ai_usage_log_recorded_at ON ai_usage_log(recorded_at);

CREATE TABLE IF NOT EXISTS ai_error_log (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    recorded_at TIMESTAMP NOT NULL,
    integration TEXT NOT NULL,
    provider TEXT,
    model TEXT,
    bot TEXT,
    session TEXT,
    round INTEGER NOT NULL DEFAULT 0,
    kind TEXT,
    message TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_ai_error_log_recorded_at ON ai_error_log(recorded_at);
