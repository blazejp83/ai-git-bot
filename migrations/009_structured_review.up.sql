-- Structured review: per-bot switch and decision thresholds. Existing bots
-- keep the free-text review (0) until switched on; new bots get it from the
-- form. NULL threshold = no limit for that severity.
ALTER TABLE bots ADD COLUMN structured_review INTEGER NOT NULL DEFAULT 0;
ALTER TABLE bots ADD COLUMN review_max_blockers INTEGER DEFAULT 0;
ALTER TABLE bots ADD COLUMN review_max_medium INTEGER DEFAULT 2;
ALTER TABLE bots ADD COLUMN review_max_low INTEGER;

CREATE TABLE IF NOT EXISTS review_records (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    bot_id INTEGER,
    repo_owner TEXT NOT NULL,
    repo_name TEXT NOT NULL,
    pr_number INTEGER NOT NULL,
    head_sha TEXT,
    runner_session_id INTEGER,
    state TEXT NOT NULL DEFAULT 'DRAFT',
    summary TEXT,
    areas_reviewed TEXT,
    limitations TEXT,
    inconclusive INTEGER NOT NULL DEFAULT 0,
    decision TEXT,
    created_at TIMESTAMP NOT NULL,
    updated_at TIMESTAMP NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_review_records_pr ON review_records(repo_owner, repo_name, pr_number);

CREATE TABLE IF NOT EXISTS review_findings (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    review_id INTEGER NOT NULL REFERENCES review_records(id) ON DELETE CASCADE,
    ordinal INTEGER NOT NULL,
    severity TEXT NOT NULL,
    category TEXT,
    title TEXT NOT NULL,
    explanation TEXT NOT NULL,
    impact TEXT,
    recommendation TEXT NOT NULL,
    locations TEXT NOT NULL DEFAULT '[]',
    withdrawn INTEGER NOT NULL DEFAULT 0,
    created_at TIMESTAMP NOT NULL,
    updated_at TIMESTAMP NOT NULL,
    UNIQUE(review_id, ordinal)
);

CREATE TABLE IF NOT EXISTS review_publications (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    review_id INTEGER NOT NULL REFERENCES review_records(id) ON DELETE CASCADE,
    event TEXT NOT NULL,
    formal INTEGER NOT NULL DEFAULT 0,
    status TEXT NOT NULL,
    error TEXT,
    created_at TIMESTAMP NOT NULL
);
