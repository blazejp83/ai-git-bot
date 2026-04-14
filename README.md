# AI-Git-Bot

[![License: MIT](https://img.shields.io/github/license/tmseidel/ai-git-bot)](LICENSE)
[![GitHub release](https://img.shields.io/github/v/release/tmseidel/ai-git-bot)](https://github.com/tmseidel/ai-git-bot/releases)
[![GitHub stars](https://img.shields.io/github/stars/tmseidel/ai-git-bot)](https://github.com/tmseidel/ai-git-bot/stargazers)

> **Your intelligent gateway between Git and AI — Half Bot, half Agent.** 

AI-Git-Bot is a lightweight, self-hostable **gateway application** that connects your Git platforms with AI providers. It receives webhooks from **Gitea, GitHub, GitHub Enterprise, GitLab, and Bitbucket Cloud**, routes them to configurable AI providers, and writes the results back as code reviews, comments, or entire pull requests.

**Now rewritten in Go** — 12MB binary, sub-second startup, ~30MB Docker image.

## What it does

**As a Bot** — automatically reviews pull requests, answers questions in comments, and delivers context-aware inline reviews. The agent explores the codebase (reads files, searches for symbols, runs tests) before reviewing, producing deeper analysis than a single-shot diff review.

**As an Agent** — autonomously implements entire issues: analyzes the task, explores the repository, writes code, validates with build tools, fixes errors, and creates a finished pull request.

## Features

- **Agentic code review** — the AI explores the repo with tools (`read_file`, `search`, `shell`) before writing its review, not just looking at the diff
- **Autonomous issue implementation** — assign an issue to the bot, it writes the code and opens a PR
- **4 AI providers** — OpenAI (with OAuth login), Anthropic, Ollama, llama.cpp
- **4 Git platforms** — Gitea, GitHub/GitHub Enterprise, GitLab, Bitbucket Cloud
- **Native tool calling** — structured tool use for OpenAI and Anthropic, OpenAI-compatible for Ollama, JSON shim for llama.cpp
- **Extended thinking** — toggle Anthropic thinking blocks or OpenAI reasoning tokens per integration
- **OpenAI OAuth login** — authenticate with your ChatGPT account instead of API keys (PKCE + device code flow)
- **Rate limit handling** — proactive budget warnings at 75/90/95% usage, graceful stop on hard limits with reset time
- **Per-bot configuration** — custom system prompts, turn limits, enable/disable agent mode per bot
- **Web admin UI** — dashboard, bot management, AI/Git integration CRUD, encrypted secret storage
- **Multi-bot support** — different bots with different prompts and providers on the same instance
- **Session persistence** — conversation history maintained per PR across updates
- **Encrypted secrets** — AES-256-GCM encryption for API keys and tokens

## Quick Start

### Option 1: Binary

```bash
# Build from source
go build -o ai-git-bot ./cmd/server/

# Run
export APP_ENCRYPTION_KEY="your-secret-key"
export DATABASE_URL="sqlite://data/aigitbot.db"
./ai-git-bot
```

### Option 2: Docker (review only, ~30MB)

```bash
docker build -f Dockerfile.go --target minimal -t ai-git-bot:minimal .
docker run -p 8080:8080 -e APP_ENCRYPTION_KEY=change-me ai-git-bot:minimal
```

### Option 3: Docker (full agent with build tools, ~1.2GB)

```bash
docker build -f Dockerfile.go --target full -t ai-git-bot .
docker run -p 8080:8080 -e APP_ENCRYPTION_KEY=change-me ai-git-bot
```

### Option 4: Docker Compose (production)

```yaml
services:
  app:
    build:
      context: .
      dockerfile: Dockerfile.go
      target: full
    ports:
      - "8080:8080"
    environment:
      DATABASE_URL: "postgres://giteabot:giteabot@db:5432/giteabot?sslmode=disable"
      APP_ENCRYPTION_KEY: ${APP_ENCRYPTION_KEY:-change-me}
      SESSION_SECRET: ${SESSION_SECRET:-change-me}
    depends_on:
      db:
        condition: service_healthy
    restart: unless-stopped

  db:
    image: postgres:17-alpine
    environment:
      POSTGRES_DB: giteabot
      POSTGRES_USER: giteabot
      POSTGRES_PASSWORD: giteabot
    volumes:
      - pgdata:/var/lib/postgresql/data
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U giteabot"]
      interval: 5s
      timeout: 5s
      retries: 5
    restart: unless-stopped

volumes:
  pgdata:
```

### Setup

1. Navigate to `http://localhost:8080`
2. Create your administrator account
3. **Create an AI Integration** — select provider, enter API key (or use "Login with OpenAI" for OAuth)
4. **Create a Git Integration** — select platform, enter URL and token
5. **Create a Bot** — link AI + Git integrations, set a system prompt, copy the webhook URL
6. **Configure webhooks** in your Git provider pointing to the bot's webhook URL

## Architecture

```
Webhook arrives (Gitea/GitHub/GitLab/Bitbucket)
  -> Parse into common event model
  -> Look up bot by webhook secret
  -> Clone repo into sandbox
  -> Start agentic loop:
       AI sees context -> calls tools -> sees results -> calls more tools -> ... -> done
  -> Post result (review comment or PR)
```

### Tool set

| Tool | Review | Implementation | Description |
|------|--------|----------------|-------------|
| `read_file` | yes | yes | Read any file in the repo |
| `list_files` | yes | yes | List files with glob patterns |
| `search` | yes | yes | grep across the codebase |
| `shell` | yes | yes | Run allowlisted commands (build, test, git, etc.) |
| `write_file` | -- | yes | Create or modify files |
| `done` | yes | yes | Signal completion with final result |

### Supported providers

| AI Provider | Tool Calling | Extended Thinking |
|------------|-------------|-------------------|
| **OpenAI** | Native `tool_calls` | `reasoning_effort` for o-series |
| **Anthropic** | Native `tool_use` blocks | `thinking` budget tokens |
| **Ollama** | OpenAI-compatible | -- |
| **llama.cpp** | JSON shim + GBNF grammar | -- |

## Configuration

All configuration via environment variables:

| Variable | Default | Description |
|----------|---------|-------------|
| `PORT` | `8080` | Server port |
| `DATABASE_URL` | `sqlite://data/aigitbot.db` | SQLite or PostgreSQL connection |
| `APP_ENCRYPTION_KEY` | *(empty)* | AES-256-GCM key for encrypting secrets |
| `SESSION_SECRET` | `change-me...` | HMAC secret for session cookies |
| `PROMPTS_DIR` | `prompts` | Directory for system prompt files |
| `AGENT_ENABLED` | `true` | Enable agent (issue implementation) |
| `AGENT_MAX_FILES` | `20` | Max files agent can modify |
| `AGENT_MAX_TOKENS` | `32768` | Max tokens per AI call |
| `AGENT_BRANCH_PREFIX` | `ai-agent/` | Branch name prefix for agent PRs |
| `AGENT_VALIDATION_ENABLED` | `true` | Run build tools for validation |
| `AGENT_VALIDATION_MAX_RETRIES` | `3` | Max validation retry loops |

## Docker images

Two build targets:

| Target | Size | Use case |
|--------|------|----------|
| `minimal` | ~30MB | Review only — no build tools |
| `full` (default) | ~1.2GB | Full agent with polyglot build tools |

The full image includes: Java 21 + Maven + Gradle, Node.js + npm + TypeScript + pnpm + yarn, Python 3, Go, Rust, C/C++ (gcc/g++/make/cmake), Ruby + Bundler.

## Tech stack

| Component | Technology |
|-----------|-----------|
| Language | Go 1.24 |
| Web framework | chi (lightweight router) |
| Database | SQLite (dev) / PostgreSQL (prod) |
| Templates | Go `html/template` + Bootstrap 5 |
| Auth | bcrypt + HMAC-signed session cookies |
| Encryption | AES-256-GCM (stdlib `crypto/aes`) |
| OAuth | OpenAI PKCE + device code flow |
| Dependencies | 3 external (chi, sqlite3, bcrypt) |

## License

[MIT](LICENSE)
