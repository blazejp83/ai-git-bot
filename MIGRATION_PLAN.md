# Migration Plan: Java/Spring Boot to Go

## Overview

Migrate ai-git-bot from Java 21 / Spring Boot 4 (~16.8K LOC, 128 files) to Go (~5-6K LOC estimated).
Add OpenAI OAuth login support (browser PKCE flow + device code flow) during migration.

### Goals
- Smaller deployment: ~20-30MB binary vs ~1.5-2GB Docker image
- Simpler codebase: ~5-6K LOC vs ~16.8K LOC
- Sub-second startup vs ~3-5 seconds
- ~20-50MB runtime memory vs ~300-400MB JVM heap
- OAuth login for OpenAI (no API key needed)

### Non-Goals
- Changing the feature set (same functionality)
- Changing the database schema conceptually (same entities, new DDL)
- Rewriting the admin UI design (same look, new templates)

---

## Go Project Structure

```
ai-git-bot/
  cmd/
    server/
      main.go                  # entrypoint, wires everything together
  internal/
    config/
      config.go                # env/file config loading (replaces application.properties)
    server/
      server.go                # HTTP server setup, middleware, routes
      middleware.go             # auth middleware, logging, recovery
    auth/
      admin.go                 # admin user management, bcrypt
      session.go               # cookie/session-based auth for web UI
      oauth.go                 # OpenAI OAuth PKCE flow (NEW)
      oauth_device.go          # OpenAI device code flow (NEW)
      oauth_token.go           # token refresh, storage, JWT parsing (NEW)
      pkce.go                  # PKCE code verifier/challenge generation (NEW)
    db/
      db.go                    # database connection (pgx or sqlx)
      migrate.go               # schema migrations (golang-migrate)
      models.go                # shared model types
    encrypt/
      encrypt.go               # AES-256-GCM encryption (port of EncryptionService)
    bot/
      bot.go                   # Bot entity, service (CRUD + webhook routing)
      webhook.go               # unified webhook handler + dispatch
    ai/
      client.go                # AiClient interface
      common.go                # shared logic: chunking, retry, message building
      openai.go                # OpenAI chat completions client
      anthropic.go             # Anthropic messages client
      ollama.go                # Ollama client
      llamacpp.go              # llama.cpp client
      provider.go              # provider registry + metadata
    repo/
      client.go                # RepositoryApiClient interface
      gitea.go                 # Gitea API client
      github.go                # GitHub API client
      gitlab.go                # GitLab API client
      bitbucket.go             # Bitbucket API client
      provider.go              # provider registry + metadata
    webhook/
      gitea.go                 # Gitea webhook payload parsing + translation
      github.go                # GitHub webhook payload parsing + translation
      gitlab.go                # GitLab webhook payload parsing + translation
      bitbucket.go             # Bitbucket webhook payload parsing + translation
      types.go                 # common WebhookEvent model
    review/
      review.go                # CodeReviewService (orchestrates AI review)
      session.go               # ReviewSession + ConversationMessage persistence
    agent/
      agent.go                 # IssueImplementationService (the big one)
      session.go               # AgentSession persistence
      diff.go                  # DiffApplyService (7 strategies)
      validation.go            # code validation orchestration
      tools.go                 # tool execution (list tree, read/write file)
    prompt/
      prompt.go                # prompt loading from markdown files
    web/
      handlers.go              # admin UI HTTP handlers (dashboard, setup, CRUD forms)
      templates.go             # template loading + rendering
  web/
    templates/
      layout.html              # base layout (port from Thymeleaf)
      dashboard.html
      login.html
      setup.html
      bots/
        list.html
        form.html
      ai-integrations/
        list.html
        form.html
      git-integrations/
        list.html
        form.html
    static/
      images/
        favicon.svg
  migrations/
    001_initial.up.sql         # create all tables
    001_initial.down.sql
    002_oauth_tokens.up.sql    # OAuth token storage (NEW)
    002_oauth_tokens.down.sql
  prompts/
    default.md
    local-llm.md
  go.mod
  go.sum
  Dockerfile
  .env.example
```

---

## Dependencies

```
# Web framework (lightweight, fast)
github.com/go-chi/chi/v5

# Database
github.com/jackc/pgx/v5          # PostgreSQL driver
github.com/mattn/go-sqlite3       # SQLite for dev (replaces H2)
github.com/golang-migrate/migrate # schema migrations

# Auth
golang.org/x/crypto/bcrypt        # password hashing
golang.org/x/oauth2               # OAuth2 client (PKCE support)

# Templates
html/template (stdlib)             # replaces Thymeleaf

# HTTP client
net/http (stdlib)                  # replaces RestClient/RestTemplate

# JSON
encoding/json (stdlib)             # replaces Jackson

# JWT parsing
github.com/golang-jwt/jwt/v5      # parse OpenAI id_token claims

# Encryption
crypto/aes, crypto/cipher (stdlib) # AES-256-GCM (replaces EncryptionService)

# Logging
log/slog (stdlib)                  # structured logging (Go 1.21+)
```

Total external dependencies: ~6 libraries. No framework beyond chi (a thin router).

---

## Migration Phases

### Phase 1: Skeleton + Database + Admin Auth
**Estimated effort: 2-3 days**

Port the foundation that everything else depends on.

#### What to build:
1. `cmd/server/main.go` — entrypoint, config loading, HTTP server startup
2. `internal/config/config.go` — read env vars and config file
   - `APP_ENCRYPTION_KEY`, `DATABASE_URL`, `PORT`, etc.
   - Replace `application.properties` with env vars + optional `.env` file
3. `internal/db/` — database connection, migrations
   - Use `pgx` for PostgreSQL, `go-sqlite3` for local dev
   - Write `001_initial.up.sql` with all 8 tables
   - No ORM — use plain SQL queries (the schema is simple CRUD)
4. `internal/encrypt/encrypt.go` — port AES-256-GCM from `EncryptionService.java`
   - Same algorithm, must produce compatible ciphertext for data migration
5. `internal/auth/admin.go` — admin user CRUD with bcrypt
6. `internal/auth/session.go` — cookie-based session auth
7. `internal/server/` — chi router setup, auth middleware
8. `internal/web/` — login page, setup page (initial admin creation)

#### Java files being replaced:
- `GiteaBotApplication.java` (14 lines) -> `cmd/server/main.go`
- `SecurityConfig.java` (69 lines) -> `internal/server/middleware.go`
- `AdminUser.java` (44 lines) -> `internal/db/models.go`
- `AdminService.java` (39 lines) -> `internal/auth/admin.go`
- `AdminUserRepository.java` (12 lines) -> SQL queries in `internal/auth/admin.go`
- `EncryptionService.java` (141 lines) -> `internal/encrypt/encrypt.go`
- `SetupController.java` (66 lines) -> `internal/web/handlers.go`
- `application.properties` (55 lines) -> `internal/config/config.go`

#### Validation:
- Can start server, see login page, create admin, log in, see empty dashboard

---

### Phase 2: OAuth Login (NEW feature)
**Estimated effort: 2-3 days**

Add OpenAI OAuth before porting the rest, since it changes how AI integrations authenticate.

#### What to build:
1. `internal/auth/pkce.go` — PKCE code verifier + challenge generation
   - 64 random bytes -> base64url (no padding) = verifier
   - SHA-256(verifier) -> base64url (no padding) = challenge
2. `internal/auth/oauth.go` — browser-based PKCE flow
   - Start temporary HTTP server on `localhost:1455`
   - Build authorization URL for `https://auth.openai.com/authorize`
   - Scopes: `openid profile email offline_access`
   - Handle callback at `/auth/callback`, exchange code for tokens
   - Token exchange: `POST https://auth.openai.com/oauth/token`
3. `internal/auth/oauth_device.go` — device code flow (for headless/SSH)
   - `POST {issuer}/api/accounts/deviceauth/usercode` -> get device code
   - Poll `POST {issuer}/api/accounts/deviceauth/token` until authorized
   - Exchange resulting authorization code via PKCE
4. `internal/auth/oauth_token.go` — token management
   - Parse JWT id_token for email, plan type, account ID
   - Refresh via `POST https://auth.openai.com/oauth/token` with `grant_type=refresh_token`
   - Client ID: `app_EMoamEEZ73f0CkXaXp7hrann`
   - Proactive refresh 30 seconds before expiry
   - Handle permanent errors: `refresh_token_expired`, `refresh_token_reused`, `refresh_token_invalidated`
5. `migrations/002_oauth_tokens.up.sql` — extend `ai_integrations` table
   - Add columns: `auth_method` (api_key | oauth), `access_token`, `refresh_token`, `id_token`, `token_expires_at`, `oauth_email`, `oauth_account_id`
6. Admin UI: "Login with OpenAI" button on AI integration form
   - Initiates OAuth flow, stores tokens on success
   - Shows logged-in email and plan type
   - Option to re-authenticate or switch to API key

#### OAuth endpoints (from codex/openclaw analysis):
```
Authorization:  https://auth.openai.com/authorize
Token exchange: POST https://auth.openai.com/oauth/token
Token refresh:  POST https://auth.openai.com/oauth/token
Device code:    POST https://auth.openai.com/api/accounts/deviceauth/usercode
Device poll:    POST https://auth.openai.com/api/accounts/deviceauth/token
Device verify:  https://auth.openai.com/codex/device
```

#### Validation:
- Can click "Login with OpenAI", browser opens, log in, see email in admin UI
- Token refresh works automatically
- Can fall back to API key entry

---

### Phase 3: AI Clients
**Estimated effort: 1-2 days**

Port the AI provider abstraction. This is clean, algorithm-heavy code that translates well.

#### What to build:
1. `internal/ai/client.go` — `AiClient` interface
   ```go
   type AiClient interface {
       ReviewDiff(ctx context.Context, req ReviewRequest) (string, error)
       Chat(ctx context.Context, messages []Message, opts ChatOpts) (string, error)
   }
   ```
2. `internal/ai/common.go` — shared logic from `AbstractAiClient.java` (288 lines)
   - Diff chunking by character count (respecting newlines)
   - Retry on "prompt too long" errors with smaller chunks
   - Message history building with system prompt
   - **This is the most important file** — all providers share this logic
3. `internal/ai/openai.go` — OpenAI Chat Completions client
   - `POST /v1/chat/completions`
   - Supports both API key auth (`Authorization: Bearer {api_key}`) and OAuth auth (`Authorization: Bearer {access_token}` + `ChatGPT-Account-ID` header)
4. `internal/ai/anthropic.go` — Anthropic Messages client
   - `POST /v1/messages` with `x-api-key` header
5. `internal/ai/ollama.go` — Ollama client
6. `internal/ai/llamacpp.go` — llama.cpp client
7. `internal/ai/provider.go` — provider metadata + registry

#### Java files being replaced:
- `AbstractAiClient.java` (288 lines) -> `internal/ai/common.go`
- `AiClient.java` (28 lines) -> `internal/ai/client.go`
- `OpenAiClient.java` (105 lines) -> `internal/ai/openai.go`
- `AnthropicAiClient.java` (104 lines) -> `internal/ai/anthropic.go`
- `OllamaClient.java` (130 lines) -> `internal/ai/ollama.go`
- `LlamaCppClient.java` (193 lines) -> `internal/ai/llamacpp.go`
- All request/response DTOs (12 files, ~540 lines) -> inline structs in each client file
- `AiProviderMetadata.java` + 4 provider metadata files (334 lines) -> `internal/ai/provider.go`

#### Validation:
- Send a test prompt to each provider, get a response
- Chunking works for large diffs
- OAuth tokens used transparently for OpenAI calls

---

### Phase 4: Git Platform Clients
**Estimated effort: 2-3 days**

Port the 4 repository API clients. Moderate complexity due to platform differences.

#### What to build:
1. `internal/repo/client.go` — `RepoClient` interface
   ```go
   type RepoClient interface {
       GetPRDiff(ctx, owner, repo string, prNum int) (string, error)
       PostComment(ctx, owner, repo string, prNum int, body string) error
       PostInlineComment(ctx, owner, repo string, prNum int, path string, line int, body string) error
       PostReaction(ctx, owner, repo string, prNum int, commentID int64, reaction string) error
       CreateBranch(ctx, owner, repo, branch, baseBranch string) error
       GetFileContent(ctx, owner, repo, path, ref string) (string, error)
       CreateOrUpdateFile(ctx, owner, repo, path, branch, content, message string) error
       DeleteFile(ctx, owner, repo, path, branch, message string) error
       ListTree(ctx, owner, repo, ref string) ([]string, error)
       CreatePR(ctx, owner, repo, title, body, head, base string) (int, error)
   }
   ```
2. `internal/repo/gitea.go` — Gitea API client (port of 264 lines)
3. `internal/repo/github.go` — GitHub API client (port of 284 lines)
   - SHA resolution for branch creation
4. `internal/repo/gitlab.go` — GitLab API client (port of 437 lines)
   - URL-encoded project paths, manual diff construction
5. `internal/repo/bitbucket.go` — Bitbucket API client (port of 336 lines)
   - Basic auth vs Bearer token handling
6. `internal/repo/provider.go` — provider metadata + registry

#### Java files being replaced:
- `RepositoryApiClient.java` (91 lines) -> `internal/repo/client.go`
- `GiteaApiClient.java` (264 lines) -> `internal/repo/gitea.go`
- `GitHubApiClient.java` (284 lines) -> `internal/repo/github.go`
- `GitLabApiClient.java` (437 lines) -> `internal/repo/gitlab.go`
- `BitbucketApiClient.java` (336 lines) -> `internal/repo/bitbucket.go`
- All review/comment model files (8 files, ~384 lines) -> inline structs
- Provider metadata files (4 files, ~412 lines) -> `internal/repo/provider.go`

#### Validation:
- Can fetch a PR diff from each platform
- Can post a comment back

---

### Phase 5: Webhook Handlers
**Estimated effort: 1-2 days**

Port webhook payload parsing and routing. Mostly data translation (JSON -> common model).

#### What to build:
1. `internal/webhook/types.go` — common `WebhookEvent` model
   ```go
   type WebhookEvent struct {
       Action     string       // opened, updated, comment, review, assigned
       EventType  string       // pull_request, issue, comment
       Repo       Repository
       PR         *PullRequest // nil for issue-only events
       Issue      *Issue       // nil for PR-only events
       Comment    *Comment
       Review     *Review
       Sender     User
   }
   ```
2. `internal/webhook/gitea.go` — parse Gitea webhook JSON -> `WebhookEvent`
3. `internal/webhook/github.go` — parse GitHub webhook JSON -> `WebhookEvent`
4. `internal/webhook/gitlab.go` — parse GitLab webhook JSON -> `WebhookEvent`
5. `internal/webhook/bitbucket.go` — parse Bitbucket webhook JSON -> `WebhookEvent`
6. `internal/bot/webhook.go` — unified endpoint: lookup bot by secret, route to parser, dispatch to review/agent service

#### Java files being replaced:
- `UnifiedWebhookController.java` (110 lines) -> `internal/bot/webhook.go`
- `GiteaWebhookHandler.java` (265 lines) -> `internal/webhook/gitea.go`
- `GitHubWebhookHandler.java` (370 lines) -> `internal/webhook/github.go`
- `GitLabWebhookHandler.java` (470 lines) -> `internal/webhook/gitlab.go`
- `BitbucketWebhookHandler.java` (282 lines) -> `internal/webhook/bitbucket.go`
- `BotWebhookService.java` (212 lines) -> `internal/bot/webhook.go`
- `WebhookPayload.java` (123 lines) -> `internal/webhook/types.go`

#### Validation:
- Send a test webhook from each platform, verify it routes correctly
- Webhook secret validation works

---

### Phase 6: Code Review Service
**Estimated effort: 1 day**

Port the review orchestration. Straightforward — fetch diff, call AI, post result.

#### What to build:
1. `internal/review/review.go` — port of `CodeReviewService.java` (451 lines)
   - `ReviewPullRequest()` — chunked AI review, post comment
   - `HandleBotCommand()` — respond to @bot mentions
   - `HandleInlineComment()` — respond to inline code comments
   - `HandleReviewSubmitted()` — respond to review comments
2. `internal/review/session.go` — review session persistence
   - Port of `SessionService.java` (156 lines)
   - Session lookup by (owner, repo, prNumber)
   - Conversation message storage
   - Context window compaction

#### Java files being replaced:
- `CodeReviewService.java` (451 lines) -> `internal/review/review.go`
- `SessionService.java` (156 lines) -> `internal/review/session.go`
- `ReviewSession.java` (68 lines) -> `internal/db/models.go`
- `ConversationMessage.java` (39 lines) -> `internal/db/models.go`
- `ReviewSessionRepository.java` (19 lines) -> SQL in `internal/review/session.go`

#### Validation:
- Open a PR, bot reviews it
- Comment on PR, bot responds
- Context carries across multiple interactions

---

### Phase 7: Agent / Issue Implementation
**Estimated effort: 3-4 days**

The largest and most complex component. Port carefully — this is where a cleaner design helps.

#### What to build:
1. `internal/agent/agent.go` — port of `IssueImplementationService.java` (1,667 lines)
   - Refactor the imperative nested-loop style into a clearer state machine:
   ```go
   type AgentState int
   const (
       StateInit AgentState = iota
       StateFetchingContext
       StateRequestingFiles
       StateGeneratingPlan
       StateValidating
       StateApplyingChanges
       StateCreatingPR
       StateDone
       StateFailed
   )
   ```
   - `HandleIssueAssigned()` — entry point
   - `HandleIssueComment()` — follow-up changes
   - `generateValidatedImplementation()` — multi-turn AI loop with file requests + tool validation
   - `executeToolValidationLoop()` — workspace prep, run validation, feed errors back to AI
   - `mergeFileChanges()` — preserve files from prior iterations
2. `internal/agent/diff.go` — port of `DiffApplyService.java` (558 lines)
   - All 7 diff application strategies (exact, line-ending, whitespace, fuzzy, collapsed, append, Levenshtein)
   - This is pure algorithm code — translates 1:1
3. `internal/agent/validation.go` — port of `CodeValidationService.java` (109 lines)
   - Syntax validation orchestration
   - Note: in Go, we can shell out to the same tools (javac, node, python, go vet) via `os/exec`
4. `internal/agent/tools.go` — port of `ToolExecutionService.java` (282 lines)
   - List tree, read file, write file tool execution
   - Security checks (path traversal prevention)
5. `internal/agent/session.go` — agent session persistence
   - Port of `AgentSessionService.java` (113 lines)

#### Design improvement over Java:
The current `IssueImplementationService.java` is 1,667 lines of imperative code with deeply nested loops. In Go, restructure as:
- A `Runner` struct holding the agent state
- Each state transition is a method that returns the next state
- The main loop is a simple `for { switch state { ... } }` — easy to follow, test, and debug
- Estimated: ~800-900 lines of Go (vs 1,667 Java)

#### Java files being replaced:
- `IssueImplementationService.java` (1,667 lines) -> `internal/agent/agent.go`
- `DiffApplyService.java` (558 lines) -> `internal/agent/diff.go`
- `CodeValidationService.java` (109 lines) -> `internal/agent/validation.go`
- `ToolExecutionService.java` (282 lines) -> `internal/agent/tools.go`
- `AgentSessionService.java` (113 lines) -> `internal/agent/session.go`
- `AgentSession.java` (140 lines) -> `internal/db/models.go`
- `AgentFileChange.java` (55 lines) -> `internal/db/models.go`
- All agent model files (114 lines) -> inline structs
- `AgentConfigProperties.java` (98 lines) -> `internal/config/config.go`

#### Validation:
- Assign an issue to bot, it generates code, creates PR
- Validation loop catches errors and retries
- Follow-up comments trigger additional changes

---

### Phase 8: Admin UI + Integration CRUD
**Estimated effort: 1-2 days**

Port the Thymeleaf templates to Go `html/template` and the CRUD handlers.

#### What to build:
1. `web/templates/*.html` — port all 10 Thymeleaf templates
   - Replace `th:each` with `{{range}}`
   - Replace `th:text` with `{{.Field}}`
   - Replace `th:if` with `{{if}}`
   - Keep Bootstrap 5.3 CSS (loaded from CDN, same as current)
2. `internal/web/handlers.go` — admin CRUD handlers
   - Dashboard (stats display)
   - Bot CRUD (list, create, edit, delete)
   - AI Integration CRUD (with OAuth login option)
   - Git Integration CRUD
3. `internal/web/templates.go` — template loading with layout inheritance
4. `internal/bot/bot.go` — Bot entity + service CRUD

#### Java files being replaced:
- `DashboardController.java` (37 lines) -> `internal/web/handlers.go`
- `BotController.java` (143 lines) -> `internal/web/handlers.go`
- `AiIntegrationController.java` (96 lines) -> `internal/web/handlers.go`
- `GitIntegrationController.java` (77 lines) -> `internal/web/handlers.go`
- `BotService.java` (60 lines) -> `internal/bot/bot.go`
- `AiIntegrationService.java` (52 lines) -> `internal/web/handlers.go`
- `GitIntegrationService.java` (65 lines) -> `internal/web/handlers.go`
- `AiClientFactory.java` (73 lines) -> `internal/ai/provider.go`
- `GiteaClientFactory.java` (81 lines) -> `internal/repo/provider.go`
- All entity classes -> `internal/db/models.go`
- All repository interfaces -> SQL queries in service files

#### Validation:
- Full admin UI works: create bots, configure integrations, view dashboard
- OAuth "Login with OpenAI" button works on AI integration form
- Encrypted storage works for API keys and tokens

---

### Phase 9: Prompt System + Config
**Estimated effort: 0.5 day**

#### What to build:
1. `internal/prompt/prompt.go` — port of `PromptService.java` (113 lines)
   - Load markdown files from `prompts/` directory
   - Fallback to embedded defaults
2. Copy `prompts/default.md` and `prompts/local-llm.md` as-is

#### Java files being replaced:
- `PromptService.java` (113 lines) -> `internal/prompt/prompt.go`
- `PromptConfig.java` (13 lines) -> `internal/config/config.go`
- `PromptConfigProperties.java` (30 lines) -> `internal/config/config.go`

---

### Phase 10: Docker + Deployment
**Estimated effort: 0.5 day**

#### What to build:
1. New `Dockerfile`:
   ```dockerfile
   # Build stage
   FROM golang:1.24-alpine AS build
   WORKDIR /app
   COPY go.mod go.sum ./
   RUN go mod download
   COPY . .
   RUN CGO_ENABLED=0 go build -o /server ./cmd/server

   # Runtime stage (with build tools for agent validation)
   FROM alpine:3.21
   RUN apk add --no-cache ca-certificates \
       # Only needed if agent feature is enabled:
       openjdk21-jre maven gradle nodejs npm python3 go gcc ruby
   COPY --from=build /server /server
   COPY migrations/ /migrations/
   COPY prompts/ /prompts/
   COPY web/ /web/
   ENTRYPOINT ["/server"]
   ```
2. Minimal image variant (no agent, no build tools):
   ```dockerfile
   FROM scratch
   COPY --from=build /server /server
   COPY migrations/ /migrations/
   COPY prompts/ /prompts/
   COPY web/ /web/
   ENTRYPOINT ["/server"]
   ```
   Result: **~20-30MB total image**
3. Update `.env.example` with Go-specific config
4. Health check: `GET /healthz` (replaces Spring Actuator)

---

### Phase 11: Tests
**Estimated effort: 2-3 days**

Port the 34 test files (~5,267 lines). Go tests are typically more concise.

#### Priority order:
1. `diff_test.go` — DiffApplyService tests (486 lines Java -> ~300 lines Go)
   - Critical: the 7 diff strategies must work identically
2. `agent_test.go` — IssueImplementationService tests (565 lines)
3. `review_test.go` — CodeReviewService tests (491 lines)
4. `webhook_test.go` — webhook integration tests (291 lines)
5. `encrypt_test.go` — encryption compatibility tests (107 lines)
   - Must verify Go can decrypt Java-encrypted data (migration path)
6. Remaining unit tests for AI clients, validation, sessions

---

## Database Migration Path

### Option A: Fresh start (recommended for new deployments)
- Run Go migrations from scratch
- Re-enter configuration through admin UI

### Option B: Data migration (for existing deployments)
- Write a one-time migration script that:
  1. Reads existing PostgreSQL tables
  2. Verifies Go encryption produces same output as Java (same AES-256-GCM algorithm)
  3. Copies all rows to new schema
  4. Existing encrypted API keys remain readable (same encryption, same key)

### Schema changes for OAuth:
```sql
ALTER TABLE ai_integrations ADD COLUMN auth_method VARCHAR(10) DEFAULT 'api_key';
ALTER TABLE ai_integrations ADD COLUMN access_token TEXT;
ALTER TABLE ai_integrations ADD COLUMN refresh_token TEXT;
ALTER TABLE ai_integrations ADD COLUMN id_token TEXT;
ALTER TABLE ai_integrations ADD COLUMN token_expires_at TIMESTAMP;
ALTER TABLE ai_integrations ADD COLUMN oauth_email VARCHAR(255);
ALTER TABLE ai_integrations ADD COLUMN oauth_account_id VARCHAR(255);
```

---

## OpenAI OAuth Reference

### Endpoints
| Purpose | Method | URL |
|---------|--------|-----|
| Authorize | GET | `https://auth.openai.com/authorize` |
| Token exchange | POST | `https://auth.openai.com/oauth/token` |
| Token refresh | POST | `https://auth.openai.com/oauth/token` |
| Device code request | POST | `https://auth.openai.com/api/accounts/deviceauth/usercode` |
| Device code poll | POST | `https://auth.openai.com/api/accounts/deviceauth/token` |
| Device verify URL | browser | `https://auth.openai.com/codex/device` |

### Authorization URL parameters
```
response_type=code
client_id=app_EMoamEEZ73f0CkXaXp7hrann
redirect_uri=http://localhost:1455/auth/callback
scope=openid profile email offline_access
code_challenge={SHA256(verifier) base64url}
code_challenge_method=S256
state={random}
```

### Token exchange (POST, form-encoded)
```
grant_type=authorization_code
code={code from callback}
redirect_uri=http://localhost:1455/auth/callback
client_id=app_EMoamEEZ73f0CkXaXp7hrann
code_verifier={verifier}
```

### Token refresh (POST, form-encoded)
```
client_id=app_EMoamEEZ73f0CkXaXp7hrann
grant_type=refresh_token
refresh_token={refresh_token}
```

### ID Token JWT claims
```json
{
  "email": "user@example.com",
  "https://api.openai.com/auth": {
    "chatgpt_plan_type": "plus",
    "chatgpt_user_id": "...",
    "chatgpt_account_id": "..."
  },
  "exp": 1234567890
}
```

### API calls with OAuth token
```
Authorization: Bearer {access_token}
ChatGPT-Account-ID: {account_id}  (optional)
```

---

## Timeline Summary

| Phase | What | Effort |
|-------|------|--------|
| 1 | Skeleton + DB + Admin Auth | 2-3 days |
| 2 | OAuth Login (NEW) | 2-3 days |
| 3 | AI Clients | 1-2 days |
| 4 | Git Platform Clients | 2-3 days |
| 5 | Webhook Handlers | 1-2 days |
| 6 | Code Review Service | 1 day |
| 7 | Agent / Issue Implementation | 3-4 days |
| 8 | Admin UI + CRUD | 1-2 days |
| 9 | Prompt System + Config | 0.5 day |
| 10 | Docker + Deployment | 0.5 day |
| 11 | Tests | 2-3 days |
| **Total** | | **~17-25 days** |

## Migration Strategy

- **Parallel operation**: Keep the Java app running in production while building Go version
- **Feature parity first**: Match all existing functionality before adding OAuth
- **Phase 2 exception**: OAuth is built early because it changes the AI client interface
- **Test each phase**: Every phase ends with a validation step before moving on
- **No big bang**: Each phase produces a working (partial) application
