-- What a bot does when assigned to an issue: implement (coding agent, still
-- gated by agent_enabled), writer (refine the issue into a spec) or none.
ALTER TABLE bots ADD COLUMN issue_workflow TEXT NOT NULL DEFAULT 'implement';
