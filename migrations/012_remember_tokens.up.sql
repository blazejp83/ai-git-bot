-- Remember-me logins: one row per browser (series). Only SHA-256 hashes of
-- the tokens are stored. The previous token stays valid briefly after a
-- rotation so parallel requests do not look like a stolen cookie.
CREATE TABLE IF NOT EXISTS remember_tokens (
    series TEXT PRIMARY KEY,
    username TEXT NOT NULL,
    token_hash TEXT NOT NULL,
    prev_token_hash TEXT,
    rotated_at TIMESTAMP,
    expires_at TIMESTAMP NOT NULL,
    last_used_at TIMESTAMP NOT NULL
);
