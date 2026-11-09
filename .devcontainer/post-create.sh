#!/usr/bin/env bash
# Runs once after the container is created, in VS Code and in Codespaces alike.
set -euo pipefail

# A fresh named volume belongs to root, which would make the mount point
# unwritable for the non-root user the container runs as.
own() {
    [ -d "$1" ] && [ ! -w "$1" ] && sudo chown -R "$(id -u):$(id -g)" "$1" || true
}
own /go/pkg/mod
# The checksum database cache lives next to the module cache; without it
# "go get" cannot verify new modules.
sudo mkdir -p /go/pkg/sumdb
own /go/pkg/sumdb
own "$HOME"

# Claude Code via the native installer: no Node required, and because it lands
# in ~/.local it can update itself without root. The home directory is a named
# volume, so binaries, logins and caches survive rebuilds and these installs
# only run on the very first create.
if ! command -v claude >/dev/null 2>&1; then
    echo "==> installing Claude Code"
    curl -fsSL https://claude.ai/install.sh | bash
fi

if ! command -v codex >/dev/null 2>&1; then
    echo "==> installing Codex CLI"
    curl -fsSL https://chatgpt.com/codex/install.sh | sh
fi

# Headless Chromium and its system libraries, installed with the playwright CLI
# bundled in the @playwright/mcp version .mcp.json runs, so the two never drift
# apart. The browser lands in ~/.cache/ms-playwright.
PLAYWRIGHT_MCP=@playwright/mcp@0.0.83
echo "==> Chromium for $PLAYWRIGHT_MCP"
playwright_cli=$(npx -y -p "$PLAYWRIGHT_MCP" which playwright)
sudo env "PATH=$PATH" "$playwright_cli" install-deps chromium >/dev/null
"$playwright_cli" install chromium

# Without a Dockerfile the aliases go in here; the marker keeps re-runs from
# appending them twice.
if ! grep -q 'alias clauderoot=' /etc/bash.bashrc; then
    echo "==> shell aliases"
    sudo tee -a /etc/bash.bashrc >/dev/null <<'ALIASES'

alias ll="ls -alF"
alias la="ls -A"
alias l="ls -CF"
alias clauderoot="claude --dangerously-skip-permissions"
alias codexroot="codex --dangerously-bypass-approvals-and-sandbox"
ALIASES
fi

echo "==> downloading modules"
go mod download

echo "==> generating templ components"
go tool templ generate

mkdir -p data

# The app prints join and participant links, so it has to know the URL people
# actually reach it on. In Codespaces that is the forwarded port, not localhost.
if [ ! -f .env ]; then
    echo "==> writing .env for local development"
    if [ -n "${CODESPACE_NAME:-}" ]; then
        base="https://${CODESPACE_NAME}-8080.${GITHUB_CODESPACES_PORT_FORWARDING_DOMAIN:-app.github.dev}"
    else
        base="http://localhost:8080"
    fi
    cat > .env <<ENV
SECRET_SANTA_BASE_URL=${base}
SECRET_SANTA_ADDR=:8080
SECRET_SANTA_DB=./data/secret-santa.db
SECRET_SANTA_ORGANIZER_TOKENS=dev:dev-organizer-token
ENV
fi

cat <<'MSG'

Ready. Useful targets:

  make watch   rebuild templ components and reload the browser on every save
  make run     start once on :8080
  make test    go test ./...
  make image   build the production container image

Both targets serve on port 8080. The development organizer token is
"dev-organizer-token" (see .env). Run "claude" to start Claude Code.

MSG

echo "==> toolchain:"
echo "    go     $(go version 2>/dev/null | awk '{print $3}' || echo '-')"
echo "    node   $(node --version 2>/dev/null || echo '-')"
echo "    claude $(command -v claude >/dev/null 2>&1 && claude --version || echo 'open a new shell')"
echo "    codex  $(command -v codex >/dev/null 2>&1 && codex --version || echo 'open a new shell')"
