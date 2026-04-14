CREATE TABLE IF NOT EXISTS admin_users (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    username TEXT NOT NULL UNIQUE,
    password_hash TEXT NOT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS ai_integrations (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL UNIQUE,
    provider_type TEXT NOT NULL,
    api_url TEXT NOT NULL,
    api_key TEXT,
    api_version TEXT,
    model TEXT NOT NULL,
    max_tokens INTEGER NOT NULL DEFAULT 4096,
    max_diff_chars_per_chunk INTEGER NOT NULL DEFAULT 120000,
    max_diff_chunks INTEGER NOT NULL DEFAULT 8,
    retry_truncated_chunk_chars INTEGER NOT NULL DEFAULT 60000,
    -- OAuth fields
    auth_method TEXT NOT NULL DEFAULT 'api_key',
    access_token TEXT,
    refresh_token TEXT,
    id_token TEXT,
    token_expires_at TIMESTAMP,
    oauth_email TEXT,
    oauth_account_id TEXT,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS git_integrations (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL UNIQUE,
    provider_type TEXT NOT NULL DEFAULT 'GITEA',
    url TEXT NOT NULL,
    username TEXT,
    token TEXT NOT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS bots (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL UNIQUE,
    username TEXT NOT NULL,
    prompt TEXT,
    webhook_secret TEXT,
    enabled BOOLEAN NOT NULL DEFAULT 1,
    ai_integration_id INTEGER NOT NULL REFERENCES ai_integrations(id),
    git_integration_id INTEGER NOT NULL REFERENCES git_integrations(id),
    agent_enabled BOOLEAN NOT NULL DEFAULT 0,
    webhook_call_count INTEGER NOT NULL DEFAULT 0,
    ai_tokens_sent INTEGER NOT NULL DEFAULT 0,
    ai_tokens_received INTEGER NOT NULL DEFAULT 0,
    last_webhook_at TIMESTAMP,
    last_ai_call_at TIMESTAMP,
    last_error_message TEXT,
    last_error_at TIMESTAMP,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS review_sessions (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    repo_owner TEXT NOT NULL,
    repo_name TEXT NOT NULL,
    pr_number INTEGER NOT NULL,
    prompt_name TEXT,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(repo_owner, repo_name, pr_number)
);

CREATE TABLE IF NOT EXISTS conversation_messages (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id INTEGER REFERENCES review_sessions(id) ON DELETE CASCADE,
    agent_session_id INTEGER REFERENCES agent_sessions(id) ON DELETE CASCADE,
    role TEXT NOT NULL,
    content TEXT NOT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS agent_sessions (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    repo_owner TEXT NOT NULL,
    repo_name TEXT NOT NULL,
    issue_number INTEGER NOT NULL,
    issue_title TEXT,
    branch_name TEXT,
    pr_number INTEGER,
    status TEXT NOT NULL DEFAULT 'IN_PROGRESS',
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(repo_owner, repo_name, issue_number)
);

CREATE TABLE IF NOT EXISTS agent_file_changes (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    agent_session_id INTEGER NOT NULL REFERENCES agent_sessions(id) ON DELETE CASCADE,
    path TEXT NOT NULL,
    operation TEXT NOT NULL,
    commit_sha TEXT,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
