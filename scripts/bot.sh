#!/usr/bin/env bash
#
# bot.sh — start / stop / restart the telegram-pc-remote bot (background daemon).
#
# Installed as `telegram-remote` in ~/.local/bin (via ./install.sh): the
# installer copies the app to ~/.local/share/telegram-remote and generates a
# small wrapper there, so `telegram-remote ...` runs the installed copy from
# anywhere (nothing is read from the repo checkout at runtime).
#
# Usage:
#   ./scripts/bot.sh start [--foreground]   start the bot (background by default)
#   telegram-remote start                    same, once installed
#   ./scripts/bot.sh stop                    stop the bot
#   ./scripts/bot.sh restart                 restart the bot
#   ./scripts/bot.sh status                  show whether the bot is running
#   ./scripts/bot.sh logs [-f]               show bot.log (follow with -f)
#   ./scripts/bot.sh reload                  live-reload commands.json + whitelist.json (SIGHUP)
#   ./scripts/bot.sh run                     run in foreground (same as start --foreground)
#   ./scripts/bot.sh config --adduser <id>   allowlist a user id (also adds it as a chat, which covers DMs)
#   ./scripts/bot.sh config --deluser <id>   remove a user id from the allowlist
#   ./scripts/bot.sh config --addchat <id>   allowlist a group/channel chat id
#   ./scripts/bot.sh config --delchat <id>   remove a chat id from the allowlist
#   ./scripts/bot.sh config [--list]         show allowlisted users + chats
#
# Details:
#   - Runs bin/telegram-pc-remote from its own directory (the Go app reads .env
#     itself). When installed, that is ~/.local/share/telegram-remote.
#   - PID file: .run/telegram-pc-remote.pid ; log file: bot.log (both git-ignored).
#   - Refuses to start without TELEGRAM_BOT_TOKEN (env or .env) or without the binary.
#   - config edits data/whitelist.json atomically (backup kept at .bak, mode 600)
#     and reloads the running bot automatically (SIGHUP).
#
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$(readlink -f "${BASH_SOURCE[0]}" 2>/dev/null || echo "${BASH_SOURCE[0]}")")" && pwd)"
ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
BIN="$ROOT/bin/telegram-pc-remote"
PIDFILE="$ROOT/.run/telegram-pc-remote.pid"
LOGFILE="$ROOT/bot.log"

fail() { echo "error: $*" >&2; exit 1; }

pid_alive() { [[ -n "${1:-}" ]] && kill -0 "$1" 2>/dev/null; }

current_pid() {
  [[ -f "$PIDFILE" ]] || return 1
  local pid
  pid="$(tr -d '[:space:]' < "$PIDFILE" 2>/dev/null || true)"
  [[ "$pid" =~ ^[0-9]+$ ]] || return 1
  pid_alive "$pid" || return 1
  printf '%s' "$pid"
}

has_token() {
  [[ -n "${TELEGRAM_BOT_TOKEN:-}" ]] && return 0
  [[ -f "$ROOT/.env" ]] || return 1
  grep -Eq '^[[:space:]]*TELEGRAM_BOT_TOKEN=[^[:space:]#]+' "$ROOT/.env"
}

ensure_built() {
  if [[ ! -x "$BIN" ]]; then
    [[ -f "$ROOT/go.mod" ]] || fail "binary not found at $BIN (reinstall with ./install.sh)"
    echo "binary not found, building..."
    (cd "$ROOT" && go build -o bin/telegram-pc-remote .) \
      || fail "build failed (run ./install.sh first)"
  fi
}

cmd_start() {
  local foreground=false
  [[ "${1:-}" == "--foreground" ]] && foreground=true

  if pid=$(current_pid); then
    echo "already running (pid $pid)"
    return 0
  fi
  # Drop a stale pid file left by a crashed process.
  [[ -f "$PIDFILE" ]] && rm -f "$PIDFILE"

  ensure_built
  has_token || fail "TELEGRAM_BOT_TOKEN is not set — export it or put it in $ROOT/.env (see .env.example)"

  if $foreground; then
    echo "running in foreground (Ctrl+C to stop)..."
    cd "$ROOT" && exec "$BIN"
  fi

  mkdir -p "$(dirname "$PIDFILE")" "$(dirname "$LOGFILE")"
  touch "$LOGFILE"
  cd "$ROOT" && nohup "$BIN" >>"$LOGFILE" 2>&1 &
  echo "$!" > "$PIDFILE"
  sleep 1
  if pid=$(current_pid); then
    echo "started (pid $pid, log: $LOGFILE)"
  else
    rm -f "$PIDFILE"
    fail "failed to start — check $LOGFILE"
  fi
}

cmd_stop() {
  local pid
  if ! pid=$(current_pid); then
    [[ -f "$PIDFILE" ]] && rm -f "$PIDFILE"
    echo "not running"
    return 0
  fi
  echo -n "stopping (pid $pid)..."
  kill "$pid" 2>/dev/null || true
  for _ in $(seq 1 50); do
    pid_alive "$pid" || break
    sleep 0.2
    echo -n "."
  done
  if pid_alive "$pid"; then
    echo -n " (forcing)"
    kill -9 "$pid" 2>/dev/null || true
    sleep 0.5
  fi
  rm -f "$PIDFILE"
  if pid_alive "$pid"; then
    fail "could not stop pid $pid"
  fi
  echo " stopped"
}

cmd_status() {
  local pid
  if pid=$(current_pid); then
    echo "running (pid $pid, log: $LOGFILE)"
  else
    echo "not running"
    return 1
  fi
}

cmd_logs() {
  [[ -f "$LOGFILE" ]] || fail "no log file yet ($LOGFILE)"
  if [[ "${1:-}" == "-f" ]]; then
    tail -f "$LOGFILE"
  else
    tail -n 50 "$LOGFILE"
  fi
}

cmd_reload() {
  local pid
  pid=$(current_pid) || fail "not running — nothing to reload"
  kill -HUP "$pid"
  echo "reloaded (SIGHUP sent to pid $pid)"
}

# --- whitelist (config) -------------------------------------------------------
# The allowlist requires BOTH the sender AND the chat to be listed. In a
# private chat with the bot both IDs are equal, so --adduser/--deluser manage
# the id in both lists; --addchat/--delchat handle group/channel chats whose
# chat id differs. DATA_DIR is honored (relative paths resolve from $ROOT).

wl_file() {
  local d="${DATA_DIR:-data}"
  [[ "$d" = /* ]] || d="$ROOT/$d"
  printf '%s/whitelist.json' "$d"
}

wl_validate() { # $1 = file; mirrors internal/whitelist (ids: non-zero integers)
  jq -e '(.users | type == "array") and (.chats | type == "array")
    and (all(.users[]; type == "number" and . != 0 and floor == .))
    and (all(.chats[]; type == "number" and . != 0 and floor == .))' "$1" >/dev/null
}

wl_ensure() {
  local f
  f="$(wl_file)"
  if [[ ! -f "$f" ]]; then
    mkdir -p "$(dirname "$f")"
    cat > "$f" <<'EOF'
{
  "note": "Allowlist. Get your numeric user/chat IDs from a bot such as @userinfobot. BOTH lists are enforced: a message is processed only if its sender is in 'users' AND the chat is in 'chats'.",
  "users": [],
  "chats": []
}
EOF
    chmod 600 "$f"
    echo "note: created empty $f"
  fi
  wl_validate "$f" || fail "whitelist file is not valid: $f"
}

# wl_commit: apply a jq filter to the whitelist file atomically (backup .bak).
wl_commit() {
  local f tmp
  f="$(wl_file)"
  tmp="$(mktemp "$(dirname "$f")/.whitelist.XXXXXX.json")"
  trap 'rm -f "$tmp"' EXIT
  jq "$@" "$f" > "$tmp" || fail "jq could not apply the change (file left untouched)"
  wl_validate "$tmp" || fail "the resulting file failed validation (file left untouched)"
  chmod 600 "$tmp"
  cp -p "$f" "$f.bak"
  chmod 600 "$f.bak"
  mv -f "$tmp" "$f"
  trap - EXIT
}

wl_valid_id() { [[ "${1:-}" =~ ^-?[1-9][0-9]*$ ]]; }

wl_present() { # $1 = id, $2 = list name (users|chats)
  local f
  f="$(wl_file)"
  jq -e --argjson id "$1" --arg list "$2" '(.[$list] // []) | index($id) != null' "$f" >/dev/null
}

wl_adduser() { # $1 = id → both lists (covers DMs where user id == chat id)
  wl_valid_id "$1" || fail "user id must be a non-zero integer (got '${1:-}')"
  wl_ensure
  if wl_present "$1" users && wl_present "$1" chats; then
    echo "note: user $1 is already allowlisted"
    return 0
  fi
  wl_commit --argjson id "$1" '
    .users |= ((. // []) + [$id] | unique)
    | .chats |= ((. // []) + [$id] | unique)'
  echo "ok: user $1 allowlisted (users + chats)"
}

wl_deluser() { # $1 = id → both lists
  wl_valid_id "$1" || fail "user id must be a non-zero integer (got '${1:-}')"
  wl_ensure
  if ! wl_present "$1" users && ! wl_present "$1" chats; then
    echo "note: user $1 was not allowlisted (nothing to do)"
    return 0
  fi
  wl_commit --argjson id "$1" '
    .users |= ((. // []) | map(select(. != $id)))
    | .chats |= ((. // []) | map(select(. != $id)))'
  echo "ok: user $1 removed from the allowlist (users + chats)"
}

wl_addchat() { # $1 = chat id → chats list only
  wl_valid_id "$1" || fail "chat id must be a non-zero integer (got '${1:-}')"
  wl_ensure
  if wl_present "$1" chats; then
    echo "note: chat $1 is already allowlisted"
    return 0
  fi
  wl_commit --argjson id "$1" '.chats |= ((. // []) + [$id] | unique)'
  echo "ok: chat $1 allowlisted"
}

wl_delchat() { # $1 = chat id → chats list only
  wl_valid_id "$1" || fail "chat id must be a non-zero integer (got '${1:-}')"
  wl_ensure
  if ! wl_present "$1" chats; then
    echo "note: chat $1 was not allowlisted (nothing to do)"
    return 0
  fi
  wl_commit --argjson id "$1" '.chats |= ((. // []) | map(select(. != $id)))'
  echo "ok: chat $1 removed from the allowlist"
}

wl_list() {
  local f
  f="$(wl_file)"
  [[ -f "$f" ]] || fail "whitelist file not found: $f (run ./install.sh first)"
  wl_validate "$f" || fail "whitelist file is not valid: $f"
  jq -r '"users (\(.users | length)): \([.users[] | tostring] | join(", "))",
          "chats (\(.chats | length)): \([.chats[] | tostring] | join(", "))"' "$f"
}

cmd_config() {
  command -v jq >/dev/null 2>&1 || fail "jq is required (install with: sudo apt install jq | brew install jq | sudo dnf install jq)"
  local dirty=false
  if [[ $# -eq 0 ]]; then
    wl_list
    return 0
  fi
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --adduser) [[ $# -ge 2 ]] || fail "config --adduser requires an ID"; wl_adduser "$2"; dirty=true; shift 2 ;;
      --deluser) [[ $# -ge 2 ]] || fail "config --deluser requires an ID"; wl_deluser "$2"; dirty=true; shift 2 ;;
      --addchat) [[ $# -ge 2 ]] || fail "config --addchat requires an ID"; wl_addchat "$2"; dirty=true; shift 2 ;;
      --delchat) [[ $# -ge 2 ]] || fail "config --delchat requires an ID"; wl_delchat "$2"; dirty=true; shift 2 ;;
      --list) wl_list; shift ;;
      -h|--help) wl_list; echo "usage: config [--adduser ID] [--deluser ID] [--addchat ID] [--delchat ID] [--list]"; return 0 ;;
      *) fail "unknown config option '$1' (try: --adduser|--deluser|--addchat|--delchat|--list)" ;;
    esac
  done
  if $dirty; then
    local pid
    if pid=$(current_pid); then
      kill -HUP "$pid" 2>/dev/null && echo "note: running bot (pid $pid) reloaded"
    else
      echo "note: bot not running — changes apply on next start"
    fi
  fi
}

usage() {
  sed -n '2,/^$/p' "$0" | sed 's/^# \?//'
}

cmd="${1:-}"
shift || true
case "$cmd" in
  start)   cmd_start "${1:-}" ;;
  run)     cmd_start --foreground ;;
  stop)    cmd_stop ;;
  restart) cmd_stop; cmd_start "${1:-}" ;;
  status)  cmd_status ;;
  logs)    cmd_logs "${1:-}" ;;
  reload)  cmd_reload ;;
  config)  cmd_config "$@" ;;
  help|-h|--help|"") usage ;;
  *) fail "unknown command '$cmd' (try: start|stop|restart|status|logs|reload|run|config)" ;;
esac
