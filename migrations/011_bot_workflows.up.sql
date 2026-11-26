-- Per-bot PR workflow settings as JSON: {"<workflow>": {"enabled": "true", "<param>": "..."}}.
-- NULL means the defaults (code review on, README sync off).
ALTER TABLE bots ADD COLUMN workflows TEXT;
