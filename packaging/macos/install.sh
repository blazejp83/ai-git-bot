#!/usr/bin/env bash
#
# Install AI Git Bot as a macOS user LaunchAgent (background service).
#
# Layout created:
#   ~/Library/Application Support/ai-git-bot/
#       bin/ai-git-bot         compiled binary
#       bin/run.sh             wrapper: sources env file, execs binary
#       migrations/            DB migrations (synced from repo each install)
#       prompts/               prompt templates (synced, preserves user edits)
#       data/                  sqlite DB lives here
#       env                    0600, APP_ENCRYPTION_KEY + other secrets
#   ~/Library/LaunchAgents/com.tmseidel.ai-git-bot.plist
#   ~/Library/Logs/ai-git-bot/{stdout,stderr}.log
#
# Reinstall is safe: an existing env file is preserved so APP_ENCRYPTION_KEY
# stays stable (rotating it would make already-encrypted secrets unreadable).
#
# Override defaults via env:
#   AI_GIT_BOT_INSTALL_DIR, AI_GIT_BOT_LOG_DIR, AI_GIT_BOT_PORT

set -euo pipefail

if [[ "$(uname -s)" != "Darwin" ]]; then
    echo "ERROR: this installer targets macOS. Detected: $(uname -s)" >&2
    exit 1
fi

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

SERVICE_LABEL="com.tmseidel.ai-git-bot"
INSTALL_DIR="${AI_GIT_BOT_INSTALL_DIR:-$HOME/Library/Application Support/ai-git-bot}"
LOG_DIR="${AI_GIT_BOT_LOG_DIR:-$HOME/Library/Logs/ai-git-bot}"
PLIST_DST="$HOME/Library/LaunchAgents/$SERVICE_LABEL.plist"
PORT="${AI_GIT_BOT_PORT:-17070}"
TEMPLATE="$SCRIPT_DIR/com.tmseidel.ai-git-bot.plist.tmpl"
UID_NUM="$(id -u)"

if [[ ! -f "$TEMPLATE" ]]; then
    echo "ERROR: plist template not found at $TEMPLATE" >&2
    exit 1
fi

if ! command -v go >/dev/null 2>&1; then
    echo "ERROR: 'go' is not on PATH. Install it first (e.g. 'brew install go')." >&2
    exit 1
fi

echo "==> Install dir: $INSTALL_DIR"
echo "==> Log dir:     $LOG_DIR"
echo "==> Port:        $PORT"

mkdir -p "$INSTALL_DIR/bin" "$INSTALL_DIR/data" "$LOG_DIR" "$HOME/Library/LaunchAgents"

echo "==> Building binary..."
( cd "$REPO_ROOT" && go build -o "$INSTALL_DIR/bin/ai-git-bot" ./cmd/server )

echo "==> Syncing migrations and prompts..."
# Migrations: authoritative from repo — delete stale files so removed migrations
# don't linger. Missing migrations don't break anything (schema_migrations tracks
# what was applied), but keeping the set clean avoids confusion.
rsync -a --delete "$REPO_ROOT/migrations/" "$INSTALL_DIR/migrations/"
# Prompts: DO NOT --delete. User may have added custom prompt files referenced
# by bot configs in the UI; wiping them on reinstall would break those bots.
rsync -a "$REPO_ROOT/prompts/" "$INSTALL_DIR/prompts/"

echo "==> Writing run wrapper..."
cat > "$INSTALL_DIR/bin/run.sh" <<'WRAPPER'
#!/bin/sh
# Wrapper invoked by launchd. Sources the env file (which holds secrets at
# mode 0600) and execs the bot binary in the install directory.
set -e
APP_DIR="$(cd "$(dirname "$0")/.." && pwd)"
cd "$APP_DIR"
if [ -f "$APP_DIR/env" ]; then
    set -a
    . "$APP_DIR/env"
    set +a
fi
exec "$APP_DIR/bin/ai-git-bot"
WRAPPER
chmod 755 "$INSTALL_DIR/bin/run.sh"

ENV_FILE="$INSTALL_DIR/env"
if [[ ! -f "$ENV_FILE" ]]; then
    echo "==> Generating env file with fresh secrets..."
    APP_ENCRYPTION_KEY="$(openssl rand -hex 32)"
    SESSION_SECRET="$(openssl rand -hex 32)"
    umask 077
    cat > "$ENV_FILE" <<EOF
# AI Git Bot environment — generated $(date)
#
# IMPORTANT: APP_ENCRYPTION_KEY must remain stable across restarts.
# Rotating it will make already-encrypted secrets (stored API keys and Git
# tokens) unreadable. Back this file up.

APP_ENCRYPTION_KEY=$APP_ENCRYPTION_KEY
SESSION_SECRET=$SESSION_SECRET
PORT=$PORT
DATABASE_URL=sqlite://data/aigitbot.db
PROMPTS_DIR=prompts

# Add further overrides below (AGENT_MAX_TOKENS, AGENT_BRANCH_PREFIX, etc.).
EOF
    chmod 600 "$ENV_FILE"
else
    echo "==> Existing env file kept (encryption key preserved)."
fi

echo "==> Rendering launchd plist..."
# Use a delimiter unlikely to appear in paths.
sed \
    -e "s|@INSTALL_DIR@|$INSTALL_DIR|g" \
    -e "s|@LOG_DIR@|$LOG_DIR|g" \
    -e "s|@HOME@|$HOME|g" \
    "$TEMPLATE" > "$PLIST_DST"
chmod 644 "$PLIST_DST"

echo "==> (Re)loading launchd service..."
# Bootout the old instance if present — ignore failure (not loaded yet).
launchctl bootout "gui/$UID_NUM/$SERVICE_LABEL" 2>/dev/null || true
launchctl bootstrap "gui/$UID_NUM" "$PLIST_DST"
launchctl enable "gui/$UID_NUM/$SERVICE_LABEL" 2>/dev/null || true
launchctl kickstart -k "gui/$UID_NUM/$SERVICE_LABEL" >/dev/null 2>&1 || true

cat <<EOF

Installed and started.

  Service:     $SERVICE_LABEL
  URL:         http://localhost:$PORT
  Install dir: $INSTALL_DIR
  Logs:        $LOG_DIR/{stdout,stderr}.log
  Env file:    $INSTALL_DIR/env   (contains APP_ENCRYPTION_KEY — back up!)

Useful commands:
  Status:   launchctl print gui/$UID_NUM/$SERVICE_LABEL | head -40
  Stop:     launchctl bootout gui/$UID_NUM/$SERVICE_LABEL
  Start:    launchctl bootstrap gui/$UID_NUM '$PLIST_DST'
  Restart:  launchctl kickstart -k gui/$UID_NUM/$SERVICE_LABEL
  Logs:     tail -f '$LOG_DIR/stderr.log'
  Uninstall: $SCRIPT_DIR/uninstall.sh

Next step: open http://localhost:$PORT and create your admin account, then
configure AI and Git integrations via the UI.
EOF
