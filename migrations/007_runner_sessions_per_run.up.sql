-- One session per run, not per target: a PR is reviewed again after each push,
-- and #investigate or a re-assignment starts another run on the same target.
-- SQLite cannot drop a constraint, so the table is rebuilt. Foreign keys are
-- switched off so dropping the old table does not cascade into runner_messages.
PRAGMA foreign_keys=OFF;
BEGIN;
CREATE TABLE runner_sessions_new (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    mode TEXT NOT NULL,
    repo_owner TEXT NOT NULL,
    repo_name TEXT NOT NULL,
    target_number INTEGER NOT NULL,
    target_type TEXT NOT NULL,
    branch_name TEXT,
    pr_number INTEGER,
    status TEXT NOT NULL DEFAULT 'IN_PROGRESS',
    turn_count INTEGER NOT NULL DEFAULT 0,
    system_prompt TEXT,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
INSERT INTO runner_sessions_new (id, mode, repo_owner, repo_name, target_number, target_type, branch_name,
    pr_number, status, turn_count, system_prompt, created_at, updated_at)
SELECT id, mode, repo_owner, repo_name, target_number, target_type, branch_name,
    pr_number, status, turn_count, system_prompt, created_at, updated_at FROM runner_sessions;
DROP TABLE runner_sessions;
ALTER TABLE runner_sessions_new RENAME TO runner_sessions;
CREATE INDEX idx_runner_sessions_target ON runner_sessions(repo_owner, repo_name, target_type, target_number, created_at);
COMMIT;
PRAGMA foreign_keys=ON;
