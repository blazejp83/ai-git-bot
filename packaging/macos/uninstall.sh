#!/usr/bin/env bash
#
# Uninstall the AI Git Bot macOS LaunchAgent.
#
# Stops and unloads the service and removes the plist.
# Does NOT delete the install dir — your sqlite DB and env file (with
# APP_ENCRYPTION_KEY) are preserved so you can reinstall without losing data.
# Remove them manually if you're sure you want a clean slate.

set -euo pipefail

if [[ "$(uname -s)" != "Darwin" ]]; then
    echo "ERROR: this uninstaller targets macOS. Detected: $(uname -s)" >&2
    exit 1
fi

SERVICE_LABEL="com.tmseidel.ai-git-bot"
PLIST_DST="$HOME/Library/LaunchAgents/$SERVICE_LABEL.plist"
INSTALL_DIR="${AI_GIT_BOT_INSTALL_DIR:-$HOME/Library/Application Support/ai-git-bot}"
LOG_DIR="${AI_GIT_BOT_LOG_DIR:-$HOME/Library/Logs/ai-git-bot}"
UID_NUM="$(id -u)"

if launchctl print "gui/$UID_NUM/$SERVICE_LABEL" >/dev/null 2>&1; then
    echo "==> Stopping service..."
    launchctl bootout "gui/$UID_NUM/$SERVICE_LABEL" || true
else
    echo "==> Service not currently loaded."
fi

if [[ -f "$PLIST_DST" ]]; then
    echo "==> Removing plist: $PLIST_DST"
    rm "$PLIST_DST"
fi

cat <<EOF

Service stopped and unloaded.

Preserved (remove manually if no longer needed):
  Install dir: $INSTALL_DIR
  Logs:        $LOG_DIR

  rm -rf "$INSTALL_DIR" "$LOG_DIR"

WARNING: $INSTALL_DIR/env holds APP_ENCRYPTION_KEY. If you delete it and
reinstall, you will not be able to decrypt any previously stored API keys
or Git tokens — you will have to re-enter them via the UI.
EOF
