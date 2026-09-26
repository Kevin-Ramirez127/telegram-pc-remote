#!/usr/bin/env bash
#
# install.sh — one-shot installer for telegram-pc-remote.
#
# What it does (idempotent, safe to re-run):
#   1. Checks for required tools: go (>= 1.23) and jq.
#   2. Builds the bot binary to bin/telegram-pc-remote.
#   3. Installs the application to $PREFIX/share/telegram-remote
#      (default PREFIX ~/.local): binary, control script, .env, data/ and
#      commands/. User content (.env token, data/*.json, commands/*.sh) is
#      seeded on first install and never overwritten afterwards.
#   4. Generates the `telegram-remote` command in $PREFIX/bin (a real file
#      running the installed copy — no dependency on this checkout).
#   5. Asks for the Telegram bot token (from @BotFather) and saves it to the
#      installed .env. Skipped when TELEGRAM_BOT_TOKEN is exported, when the
#      installed .env already has a token, when stdin is not a TTY, or with
#      --no-prompt.
#   6. Makes scripts executable.
#
# Usage:
#   ./install.sh                          # full install (prompts for token if needed)
#   ./install.sh --check                  # also run `go vet` + `go test ./...` after building
#   ./install.sh --no-prompt              # never prompt (for scripts/CI)
#   TELEGRAM_BOT_TOKEN=... ./install.sh   # provide the token non-interactively
#   PREFIX=/opt/tpr ./install.sh          # custom install prefix
#   INSTALL_DIR=... APP_DIR=... ./install.sh  # override install locations directly
#
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$ROOT_DIR"

PREFIX="${PREFIX:-$HOME/.local}"
INSTALL_DIR="${INSTALL_DIR:-$PREFIX/bin}"
APP_DIR="${APP_DIR:-$PREFIX/share/telegram-remote}"
INSTALL_NAME="telegram-remote"

CHECK=false
NO_PROMPT=false
fail() { echo "error: $*" >&2; exit 1; }
info() { echo "==> $*"; }
for arg in "$@"; do
  case "$arg" in
    --check) CHECK=true ;;
    --no-prompt|--non-interactive) NO_PROMPT=true ;;
    -h|--help) sed -n '2,/^$/p' "$0" | sed 's/^# \?//'; exit 0 ;;
    *) fail "unknown option '$arg' (try --help)" ;;
  esac
done

# --- 1. prerequisites ---------------------------------------------------------
info "checking prerequisites..."

command -v go >/dev/null 2>&1 || fail "go is not installed (see https://go.dev/dl/ — need go >= 1.23)"
GO_VER="$(go version | grep -oE 'go[0-9]+\.[0-9]+' | tr -d 'go' || echo 0)"
info "found $(go version)"

command -v jq >/dev/null 2>&1 || fail "jq is required (install with: sudo apt install jq | brew install jq | sudo dnf install jq)"

# --- 2. build -----------------------------------------------------------------
info "building bin/telegram-pc-remote..."
mkdir -p bin
go mod download
if ! go build -o bin/telegram-pc-remote .; then
  fail "build failed"
fi
chmod +x bin/telegram-pc-remote
info "build ok: bin/telegram-pc-remote"

# --- 3. repo .env (dev convenience; the installed copy is what runs) ---------
if [[ ! -f .env ]]; then
  [[ -f .env.example ]] || fail ".env.example is missing"
  cp .env.example .env
  chmod 600 .env
  info "created .env from .env.example (chmod 600)"
else
  chmod 600 .env
  info ".env already exists (kept, chmod 600 enforced)"
fi

# --- 4. token helpers (operate on any .env file; never echo the token) -----------
env_file_token() { # $1 = env file; prints the token or returns 1
  [[ -f "$1" ]] || return 1
  local v
  v="$(sed -n 's/^[[:space:]]*TELEGRAM_BOT_TOKEN=[[:space:]]*//p' "$1" | head -n 1)"
  v="$(printf '%s' "$v" | tr -d '[:space:]' | tr -d "\"'")"
  [[ -n "$v" ]] && printf '%s' "$v"
}

set_env_file_token() { # $1 = env file, $2 = token; upserts the token (mode 600)
  local f="$1" t="$2" t_esc tmp
  t_esc="$(printf '%s' "$t" | sed 's/[&\\/]/\\&/g')"
  tmp="$(mktemp "$(dirname "$f")/.env.XXXXXX")"
  trap 'rm -f "$tmp"' EXIT
  if grep -Eq '^[[:space:]]*TELEGRAM_BOT_TOKEN=' "$f" 2>/dev/null; then
    sed -E "s|^[[:space:]]*TELEGRAM_BOT_TOKEN=.*|TELEGRAM_BOT_TOKEN=${t_esc}|" "$f" > "$tmp"
  else
    cat "$f" > "$tmp" 2>/dev/null || true
    [[ -s "$tmp" && -n "$(tail -c 1 "$tmp")" ]] && printf '\n' >> "$tmp"
    printf 'TELEGRAM_BOT_TOKEN=%s\n' "$t" >> "$tmp"
  fi
  chmod 600 "$tmp"
  mv -f "$tmp" "$f"
  trap - EXIT
}

# --- 5. install application to $APP_DIR ----------------------------------------
# Program files are always refreshed; user content (.env, data, commands) is
# only seeded when missing, so reinstalls never wipe configuration.
info "installing application to $APP_DIR..."
mkdir -p "$APP_DIR/bin" "$APP_DIR/scripts" "$APP_DIR/data" "$APP_DIR/commands"
install -m755 bin/telegram-pc-remote "$APP_DIR/bin/telegram-pc-remote"
install -m755 scripts/bot.sh "$APP_DIR/scripts/bot.sh"
info "installed binary + control script"

shopt -s nullglob
for src in commands/*.sh; do
  base="$(basename "$src")"
  if [[ ! -f "$APP_DIR/commands/$base" ]]; then
    cp -p "$src" "$APP_DIR/commands/$base"
    info "installed commands/$base (new file)"
  fi
done
shopt -u nullglob

seed_json() { # $1 = filename; copies repo data file only when missing
  if [[ ! -f "$APP_DIR/data/$1" ]]; then
    cp -p "data/$1" "$APP_DIR/data/$1"
    info "installed data/$1 (new file)"
  fi
  chmod 600 "$APP_DIR/data/$1"
}

mkdir -p data commands
if [[ ! -f data/commands.json ]]; then
  printf '{\n  "version": 2,\n  "commands": []\n}\n' > data/commands.json
  chmod 600 data/commands.json
  info "created empty repo data/commands.json (chmod 600)"
else
  chmod 600 data/commands.json
  jq -e '.version == 2 and (.commands | type == "array")' data/commands.json >/dev/null \
    || fail "repo data/commands.json is not valid (expected {version: 2, commands: []})"
  info "repo data/commands.json valid (kept)"
fi
seed_json commands.json

if [[ ! -f data/whitelist.json ]]; then
  cat > data/whitelist.json <<'EOF'
{
  "note": "Allowlist. Get your numeric user/chat IDs from a bot such as @userinfobot. BOTH lists are enforced: a message is processed only if its sender is in 'users' AND the chat is in 'chats'.",
  "users": [],
  "chats": []
}
EOF
  chmod 600 data/whitelist.json
  info "created repo data/whitelist.json skeleton (chmod 600)"
else
  chmod 600 data/whitelist.json
  info "repo data/whitelist.json kept (chmod 600 enforced)"
fi
seed_json whitelist.json

# Seed the installed .env: prefer the repo .env token, else the example file.
# An existing installed .env is never touched here.
if [[ ! -f "$APP_DIR/.env" ]]; then
  if t="$(env_file_token .env)"; then
    printf '# Installed by telegram-pc-remote/install.sh. Never commit this file.\nTELEGRAM_BOT_TOKEN=%s\n' "$t" > "$APP_DIR/.env"
    info "installed .env (token taken from repo .env)"
  else
    cp .env.example "$APP_DIR/.env"
    info "installed .env from .env.example (no token yet)"
  fi
  chmod 600 "$APP_DIR/.env"
  unset t
fi

# --- 6. bot token (installed .env) ----------------------------------------------
# Sources, in order: exported env var, installed .env value, interactive prompt.
if [[ -n "${TELEGRAM_BOT_TOKEN:-}" ]]; then
  set_env_file_token "$APP_DIR/.env" "$TELEGRAM_BOT_TOKEN"
  info "bot token taken from environment and saved to installed .env (chmod 600)"
elif env_file_token "$APP_DIR/.env" >/dev/null; then
  info "bot token already configured in installed .env (kept)"
elif [[ "$NO_PROMPT" == "true" ]]; then
  info "no token configured — set TELEGRAM_BOT_TOKEN in $APP_DIR/.env later (--no-prompt given)"
elif [[ ! -t 0 ]]; then
  info "no token configured — no TTY for prompting; set TELEGRAM_BOT_TOKEN in $APP_DIR/.env later"
else
  printf 'Enter your Telegram bot token from @BotFather (empty to skip): ' >&2
  token=""
  read -rs token || true
  echo >&2
  token="$(printf '%s' "$token" | tr -d '[:space:]')"
  if [[ -z "$token" ]]; then
    info "skipped — set TELEGRAM_BOT_TOKEN in $APP_DIR/.env later"
  else
    [[ "$token" =~ ^[0-9]+:[A-Za-z0-9_-]+$ ]] || echo "warning: that does not look like a Telegram token (expected digits, a colon, then the secret)" >&2
    set_env_file_token "$APP_DIR/.env" "$token"
    info "bot token saved to installed .env (chmod 600)"
  fi
  unset token
fi

# --- 7. scripts ----------------------------------------------------------------
chmod +x scripts/*.sh install.sh 2>/dev/null || true
info "scripts are executable"

# --- 8. install `telegram-remote` command (real file, runs installed copy) ------
info "installing $INSTALL_NAME into $INSTALL_DIR..."
mkdir -p "$INSTALL_DIR"
rm -f "$INSTALL_DIR/$INSTALL_NAME"  # drop the legacy symlink, if present
cat > "$INSTALL_DIR/$INSTALL_NAME" <<EOF
#!/usr/bin/env bash
# Generated by telegram-pc-remote/install.sh — do not edit (reinstall to update).
exec "$APP_DIR/scripts/bot.sh" "\$@"
EOF
chmod +x "$INSTALL_DIR/$INSTALL_NAME"
info "installed $INSTALL_DIR/$INSTALL_NAME (runs $APP_DIR)"
case ":${PATH:-}:" in
  *":$INSTALL_DIR:"*) ;;
  *) echo "note: $INSTALL_DIR is not on your PATH — add 'export PATH=\"\$HOME/.local/bin:\$PATH\"' to ~/.bashrc (then restart the shell)" >&2 ;;
esac

# --- 9. optional verification ---------------------------------------------------
if $CHECK; then
  info "running go vet..."
  go vet ./...
  info "running go test ./..."
  go test ./...
  info "checks passed"
fi

# --- next steps -----------------------------------------------------------------
cat <<'EOF'

install done. Next steps:

  Installed layout (self-contained, repo not needed at runtime):
    ~/.local/bin/telegram-remote              # the command
    ~/.local/share/telegram-remote/           # app root: bin/, scripts/, .env,
                                              # data/, commands/, bot.log, .run/

  1. Bot token: configured above (or edit ~/.local/share/telegram-remote/.env).

  2. Whitelist yourself (numeric IDs — get them from @userinfobot):
       telegram-remote config --adduser <your-id>
       telegram-remote config --list

  3. Register a button (example):
       COMMANDS_FILE=~/.local/share/telegram-remote/data/commands.json \
       COMMANDS_DIR=~/.local/share/telegram-remote/commands \
         scripts/manage_commands.sh add "Hello" --script hello.sh

  4. Start / stop the bot (works from any directory):
       telegram-remote start
       telegram-remote status
       telegram-remote logs
       telegram-remote stop
       # or via make (repo checkout): make start | make stop | make status

  To remove the command later: make uninstall  (keeps .env, data/, commands/)

EOF
