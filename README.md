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
- **5 AI providers** — Codex CLI, Gemini CLI, Anthropic, Ollama, llama.cpp
- **4 Git platforms** — Gitea, GitHub/GitHub Enterprise, GitLab, Bitbucket Cloud
- **CLI instrumentation** — Codex and Gemini run as non-interactive subprocesses; auth is handled by the CLIs themselves (no API keys needed)
- **Native tool calling** — structured tool use for Anthropic, OpenAI-compatible for Ollama, JSON shim for llama.cpp; CLI providers run in full-auto mode
- **Extended thinking** — toggle Anthropic thinking blocks per integration
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

### CLI providers in Docker

The Codex and Gemini providers work by invoking the respective CLIs as subprocesses. When running in Docker, the CLI binaries and their auth directories must be mounted from the host:

```yaml
volumes:
  # Codex CLI (OpenAI) — authenticate on host first: codex auth login
  - ${HOME}/.local/bin/codex:/usr/local/bin/codex:ro
  - ${HOME}/.codex:/home/app/.codex:ro

  # Gemini CLI (Google) — authenticate on host first: gemini auth login
  - /usr/local/bin/gemini:/usr/local/bin/gemini:ro
  - ${HOME}/.gemini:/home/app/.gemini:ro
```

Only mount the providers you plan to use. If a CLI is not available at runtime, the integration will return a clear error with setup instructions. Anthropic, Ollama, and llama.cpp use direct API calls and work without any mounts.

### Setup

1. Navigate to `http://localhost:8080`
2. Create your administrator account
3. **Create an AI Integration** — select provider (Codex/Gemini need no API key; Anthropic requires one)
4. **Create a Git Integration** — select platform, enter URL and token
5. **Create a Bot** — link AI + Git integrations, set a system prompt, copy the webhook URL
6. **Configure webhooks** in your Git provider pointing to the bot's webhook URL

## Git Platform Setup

Each bot needs its own user account on the Git platform. That account posts review comments, creates PRs, and receives @mentions.

### Gitea

1. **Create a bot user** on your Gitea instance (e.g. `ai-bot`)
2. **Grant access** — add the bot user as a collaborator (write) on repos you want reviewed
3. **Generate an API token** — log in as the bot user, go to Settings > Applications > Generate Token
4. **In AI Git Bot:**
   - Create a **Git Integration**: provider `GITEA`, URL `https://your-gitea.example.com`, paste the token
   - Create a **Bot**: set username to `ai-bot`, link to the Git + AI integrations
   - Copy the bot's **Webhook URL** (shown on the bot edit page)
5. **Configure webhook in Gitea:**
   - Go to repo Settings > Webhooks > Add Webhook > Gitea
   - Paste the webhook URL
   - Content Type: `application/json`
   - Events: select "Pull Requests", "Issue Comments", and optionally "Issues" (for agent)
   - Save

### GitHub

1. **Create a bot account** on GitHub (e.g. `ai-reviewer-bot`)
2. **Invite as collaborator** on your repos (with write access)
3. **Generate a PAT** — log in as the bot, go to Settings > Developer settings > Personal access tokens > Fine-grained tokens
   - Repository access: select your repos
   - Permissions: Contents (read/write), Pull requests (read/write), Issues (read/write)
4. **In AI Git Bot:**
   - Create a **Git Integration**: provider `GITHUB`, URL `https://api.github.com`, paste the PAT
   - Create a **Bot**: set username to `ai-reviewer-bot`, link to the Git + AI integrations
   - Copy the bot's **Webhook URL**
5. **Configure webhook in GitHub:**
   - Go to repo Settings > Webhooks > Add webhook
   - Payload URL: paste the webhook URL
   - Content type: `application/json`
   - Events: select "Pull requests", "Issue comments", "Pull request reviews", and optionally "Issues"
   - Save

### GitLab

1. **Create a bot user** on your GitLab instance (e.g. `ai-bot`)
2. **Add as project member** with Developer role (or higher)
3. **Generate a PAT** — log in as the bot, go to Settings > Access Tokens
   - Scopes: `api`, `read_repository`
4. **In AI Git Bot:**
   - Create a **Git Integration**: provider `GITLAB`, URL `https://gitlab.com` (or your self-hosted URL), paste the PAT
   - Create a **Bot**: set username to `ai-bot`, link to the Git + AI integrations
   - Copy the bot's **Webhook URL**
5. **Configure webhook in GitLab:**
   - Go to project Settings > Webhooks
   - URL: paste the webhook URL
   - Triggers: select "Merge request events", "Note events", and optionally "Issue events"
   - Save

### Bitbucket Cloud

1. **Create a bot account** on Bitbucket (or use a workspace service account)
2. **Grant repo access** — add the bot as a collaborator with write permissions
3. **Create an App Password** — log in as the bot, go to Personal settings > App passwords
   - Permissions: Repositories (read/write), Pull requests (read/write)
4. **In AI Git Bot:**
   - Create a **Git Integration**: provider `BITBUCKET`, URL `https://api.bitbucket.org/2.0`, set username + app password
   - Create a **Bot**: set username to the bot's Bitbucket username, link to the Git + AI integrations
   - Copy the bot's **Webhook URL**
5. **Configure webhook in Bitbucket:**
   - Go to repo Settings > Webhooks > Add webhook
   - URL: paste the webhook URL
   - Triggers: select "Pull request: Created", "Pull request: Updated", "Pull request: Comment created"
   - Save

### Multi-platform setup

You can run multiple bots on the same AI Git Bot instance — one per Git platform. All bots can share the same AI Integration:

```
AI Integration: "Codex"
  |
  ├── Git Integration: "My Gitea"     → Bot: "gitea-reviewer"  (username: ai-bot)
  └── Git Integration: "My GitHub"    → Bot: "github-reviewer" (username: ai-reviewer-bot)
```

Each bot gets its own webhook URL. Configure webhooks in each platform pointing to the respective bot's URL.

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

| AI Provider | Integration | Tool Calling | Extended Thinking |
|------------|-------------|-------------|-------------------|
| **Codex CLI** | CLI subprocess (`codex exec`) | Full-auto mode (CLI handles tools) | -- |
| **Gemini CLI** | CLI subprocess (`gemini`) | Full-auto mode (CLI handles tools) | -- |
| **Anthropic** | Direct API | Native `tool_use` blocks | `thinking` budget tokens |
| **Ollama** | Direct API | OpenAI-compatible `tool_calls` | -- |
| **llama.cpp** | Direct API | JSON shim + GBNF grammar | -- |

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
| CLI providers | Codex CLI, Gemini CLI (non-interactive subprocess) |
| Dependencies | 3 external (chi, sqlite3, bcrypt) |

## License

[MIT](LICENSE)
