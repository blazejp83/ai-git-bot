ALTER TABLE ai_integrations ADD COLUMN extended_thinking BOOLEAN NOT NULL DEFAULT 0;
ALTER TABLE ai_integrations ADD COLUMN thinking_budget INTEGER NOT NULL DEFAULT 10000;
