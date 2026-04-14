CREATE TABLE IF NOT EXISTS runner_sessions (
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
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(repo_owner, repo_name, target_type, target_number)
);

CREATE TABLE IF NOT EXISTS runner_messages (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    runner_session_id INTEGER NOT NULL REFERENCES runner_sessions(id) ON DELETE CASCADE,
    role TEXT NOT NULL,
    message_type TEXT NOT NULL,
    content TEXT,
    tool_call_id TEXT,
    tool_name TEXT,
    tool_input TEXT,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
