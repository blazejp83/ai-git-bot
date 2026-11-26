-- Feedback on structured reviews: "@bot feedback R12-F2 incorrect <reason>".
-- id is the FB number. status: active, superseded (a later verdict by the same
-- person on the same target) or retracted.
ALTER TABLE bots ADD COLUMN feedback_enabled INTEGER NOT NULL DEFAULT 1;

CREATE TABLE IF NOT EXISTS review_feedback (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    bot_id INTEGER NOT NULL,
    repo_owner TEXT NOT NULL,
    repo_name TEXT NOT NULL,
    pr_number INTEGER NOT NULL,
    review_id INTEGER NOT NULL REFERENCES review_records(id) ON DELETE CASCADE,
    finding_ordinal INTEGER,          -- NULL = the whole review or its assessment
    target TEXT NOT NULL,             -- R12, R12-F2 or R12-A
    verdict TEXT NOT NULL,            -- useful, incorrect, irrelevant, needs-improvement
    reason TEXT,
    author TEXT NOT NULL,
    author_id TEXT,
    comment_id INTEGER,
    status TEXT NOT NULL DEFAULT 'active',
    created_at TIMESTAMP NOT NULL,
    updated_at TIMESTAMP NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_review_feedback_repo ON review_feedback(bot_id, repo_owner, repo_name, status);
-- One record per comment: a redelivered webhook does not record twice.
CREATE UNIQUE INDEX IF NOT EXISTS idx_review_feedback_comment ON review_feedback(bot_id, comment_id) WHERE comment_id IS NOT NULL;
