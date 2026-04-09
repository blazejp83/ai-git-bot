# Migration Guide: 1.0.0 → 1.1.0

This guide helps you upgrade AI Gitea Bot from version 1.0.0 to 1.1.0, which introduces a web-based management UI with multi-bot support.

## What's New in 1.1.0

- **Web Dashboard**: Manage bots, AI integrations, and Git integrations through a browser UI
- **Multi-Bot Support**: Create and configure multiple bots, each with their own webhook URL
- **AI Integration Management**: Configure multiple AI providers (Anthropic, OpenAI, Ollama, llama.cpp) via UI
- **Git Integration Management**: Configure Git providers (Gitea) via UI
- **Encrypted Secrets**: API keys and tokens are encrypted at rest in the database
- **Admin Authentication**: Secure the management UI with username/password authentication
- **Per-Bot Statistics**: Track webhook calls, AI token usage, and errors per bot

## Migration Steps

### 1. Set an Encryption Key

The new version encrypts sensitive data (API keys, tokens) in the database. You **must** set a persistent encryption key:

```yaml
# docker-compose.yml
services:
  bot:
    environment:
      APP_ENCRYPTION_KEY: "your-secure-random-key-here"
```

> **Important:** If you don't set this key, a random key is generated at startup and encrypted data will be lost on restart. Generate a strong key, e.g.: `openssl rand -base64 32`

### 2. Update Your Docker Compose

The existing environment variables (`GITEA_URL`, `GITEA_TOKEN`, `AI_PROVIDER`, etc.) continue to work as before for the default bot behavior. The new UI features are additive.

```yaml
version: '3.8'
services:
  bot:
    image: tmseidel/ai-gitea-bot:1.1.0
    environment:
      # Existing configuration (still works)
      GITEA_URL: http://gitea:3000
      GITEA_TOKEN: your-gitea-token
      AI_PROVIDER: anthropic
      AI_MODEL: claude-sonnet-4-20250514
      AI_MAX_TOKENS: 4096
      AI_ANTHROPIC_API_KEY: your-api-key
      BOT_USERNAME: ai_bot
      DATABASE_URL: jdbc:postgresql://db:5432/giteabot
      DATABASE_USERNAME: giteabot
      DATABASE_PASSWORD: change-me
      # New in 1.1.0
      APP_ENCRYPTION_KEY: your-secure-encryption-key
    ports:
      - "8080:8080"
    depends_on:
      db:
        condition: service_healthy

  db:
    image: postgres:17-alpine
    environment:
      POSTGRES_DB: giteabot
      POSTGRES_USER: giteabot
      POSTGRES_PASSWORD: change-me
    volumes:
      - pgdata:/var/lib/postgresql/data
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U giteabot"]
      interval: 5s
      timeout: 5s
      retries: 5

volumes:
  pgdata:
```

### 3. Database Migration

The application automatically creates the new database tables on startup (`spring.jpa.hibernate.ddl-auto=update`). No manual database migration is needed.

New tables added:
- `admin_users` - Administrator accounts
- `ai_integrations` - AI provider configurations
- `git_integrations` - Git provider configurations
- `bots` - Bot configurations with webhook URLs and statistics

### 4. Initial Setup

After upgrading, visit `http://your-server:8080/setup` to create your administrator account. This is a one-time setup step.

### 5. Configure Bots via UI (Optional)

After logging in, you can:
1. Create AI Integrations matching your current provider settings
2. Create Git Integrations matching your Gitea configuration
3. Create Bots that combine an AI + Git integration with a unique webhook URL

### 6. Backward Compatibility

The existing `/api/webhook` endpoint continues to work exactly as before using your environment variable configuration. The new per-bot webhook URLs (`/api/webhook/{secret}`) are additional endpoints for bots created through the UI.

## Breaking Changes

None. The upgrade is fully backward compatible.

## New Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `APP_ENCRYPTION_KEY` | (random) | Encryption key for sensitive data. Set this for production! |

## Rollback

If you need to roll back to 1.0.0:
1. Stop the application
2. Use the 1.0.0 Docker image
3. The new database tables will be ignored by the older version
4. Your existing configuration via environment variables will continue to work
