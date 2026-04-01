# Architecture

This document describes the high-level architecture of the Anthropic Gitea Bot, including component responsibilities and request flows.

## System Overview

```mermaid
graph LR
    Gitea["Gitea Instance"]
    Bot["Anthropic Gitea Bot"]
    Anthropic["Anthropic API"]

    Gitea -- "Webhook (PR event)" --> Bot
    Bot -- "Fetch PR diff" --> Gitea
    Bot -- "Review diff" --> Anthropic
    Anthropic -- "Review text" --> Bot
    Bot -- "Post review comment" --> Gitea
```

The bot sits between a Gitea instance and the Anthropic Claude API. When a pull request is opened or updated, Gitea sends a webhook to the bot. The bot fetches the diff, sends it to Claude for review, and posts the review back as a PR comment.

## Component Diagram

```mermaid
graph TD
    subgraph "Spring Boot Application"
        Controller["GiteaWebhookController<br/><i>REST endpoint</i>"]
        ReviewService["CodeReviewService<br/><i>Orchestration</i>"]
        PromptService["PromptService<br/><i>Prompt resolution</i>"]
        GiteaClient["GiteaApiClient<br/><i>Gitea REST calls</i>"]
        AnthropicClient["AnthropicClient<br/><i>Claude API calls</i>"]
        AppConfig["AppConfig<br/><i>RestClient beans</i>"]
        PromptConfig["PromptConfigProperties<br/><i>Prompt definitions</i>"]
    end

    subgraph "External"
        Gitea["Gitea"]
        Anthropic["Anthropic API"]
        PromptFiles["Prompt Files<br/><i>prompts/*.md</i>"]
    end

    Controller --> ReviewService
    ReviewService --> PromptService
    ReviewService --> GiteaClient
    ReviewService --> AnthropicClient
    PromptService --> PromptConfig
    PromptService --> PromptFiles
    GiteaClient --> Gitea
    AnthropicClient --> Anthropic
    AppConfig --> GiteaClient
    AppConfig --> AnthropicClient
```

## Components

### GiteaWebhookController

- **Package:** `org.remus.giteabot.gitea`
- **Endpoint:** `POST /api/webhook?prompt={name}`
- Receives Gitea webhook payloads for pull request events
- Filters for `opened` and `synchronized` actions only
- Accepts an optional `prompt` query parameter to select a review profile
- Delegates to `CodeReviewService` asynchronously

### CodeReviewService

- **Package:** `org.remus.giteabot.review`
- Orchestrates the full review flow:
  1. Resolves prompt configuration (system prompt, model override, token override)
  2. Fetches the PR diff from Gitea
  3. Sends the diff to Claude for review
  4. Posts the review comment back to the PR
- Runs asynchronously via `@Async`

### PromptService

- **Package:** `org.remus.giteabot.config`
- Resolves named prompt definitions from configuration
- Loads system prompt content from markdown files on disk
- Falls back to the `default` definition, then to a hardcoded built-in prompt
- Resolves per-prompt model and Gitea token overrides

### AnthropicClient

- **Package:** `org.remus.giteabot.anthropic`
- Sends review requests to the Anthropic Messages API
- Handles large diffs by splitting them into chunks
- Retries with truncated input when prompts exceed model limits
- Supports system prompt and model overrides per request

### GiteaApiClient

- **Package:** `org.remus.giteabot.gitea`
- Fetches PR diffs from the Gitea API
- Posts review comments back to PRs
- Supports per-request token overrides with cached `RestClient` instances

### AppConfig

- **Package:** `org.remus.giteabot.config`
- Configures `RestClient` beans for Gitea and Anthropic API communication

### PromptConfigProperties

- **Package:** `org.remus.giteabot.config`
- Maps `prompts.*` configuration properties to named `PromptConfig` definitions
- Each definition specifies a markdown file and optional model/token overrides

## Request Flow

```mermaid
sequenceDiagram
    participant Gitea
    participant Controller as WebhookController
    participant Review as CodeReviewService
    participant Prompt as PromptService
    participant GiteaAPI as GiteaApiClient
    participant Claude as AnthropicClient

    Gitea->>Controller: POST /api/webhook?prompt=security
    Controller->>Review: reviewPullRequest(payload, "security")
    Review->>Prompt: resolveGiteaToken("security")
    Prompt-->>Review: token override (or null)
    Review->>GiteaAPI: getPullRequestDiff(owner, repo, pr, token)
    GiteaAPI->>Gitea: GET /api/v1/repos/.../pulls/{n}.diff
    Gitea-->>GiteaAPI: diff content
    GiteaAPI-->>Review: diff string
    Review->>Prompt: getSystemPrompt("security")
    Prompt-->>Review: prompt from security-review.md
    Review->>Prompt: resolveModel("security")
    Prompt-->>Review: claude-opus-4-20250514
    Review->>Claude: reviewDiff(title, body, diff, prompt, model)
    Claude-->>Review: review text
    Review->>GiteaAPI: postReviewComment(owner, repo, pr, comment, token)
    GiteaAPI->>Gitea: POST /api/v1/repos/.../pulls/{n}/reviews
    Gitea-->>GiteaAPI: 200 OK
```

## Diff Chunking Flow

```mermaid
flowchart TD
    A[Receive full diff] --> B{Diff size > max chunk chars?}
    B -- No --> C[Send as single chunk]
    B -- Yes --> D[Split at newline boundaries]
    D --> E{More chunks && under limit?}
    E -- Yes --> D
    E -- No --> F[Review each chunk]
    F --> G{API returns 'prompt too long'?}
    G -- No --> H[Collect review]
    G -- Yes --> I[Truncate and retry]
    I --> H
    H --> J[Combine all chunk reviews]
```

## Prompt Resolution Flow

```mermaid
flowchart TD
    A["Webhook arrives with ?prompt=name"] --> B{name provided?}
    B -- No --> C[Look up 'default' definition]
    B -- Yes --> D[Look up named definition]
    D --> E{Definition found?}
    E -- No --> C
    E -- Yes --> F[Load markdown file from prompts.dir]
    C --> G{Default definition exists?}
    G -- No --> H[Use hardcoded built-in prompt]
    G -- Yes --> F
    F --> I{File readable?}
    I -- No --> H
    I -- Yes --> J[Return file content as system prompt]
```

## Docker Deployment

```mermaid
graph LR
    subgraph "Docker Container"
        App["app.jar<br/>(Spring Boot)"]
        Prompts["/app/prompts/<br/>Mounted volume"]
    end

    Host["Host filesystem<br/>./prompts/"] -- "bind mount :ro" --> Prompts
    App -- reads --> Prompts
```

- The `prompts/` directory is baked into the image with a default prompt
- At runtime, the host's `./prompts/` directory is bind-mounted as read-only
- Prompt files can be edited on the host without rebuilding the image
