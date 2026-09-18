#!/usr/bin/env bash
#
# manage_commands.sh — register / list / edit / delete bot command buttons.
#
# Commands are user-authored shell scripts living in the commands directory
# (../commands by default). Registering a command stores a small record in the
# commands file (data/commands.json by default):
#
#   text         button label AND the exact text to match (1-64 chars)
#   script       the .sh file to run, relative to the commands directory
#   template     optional response template; ${output} is replaced by the
#                script's stdout, \n and \t become real newlines/tabs
#   img          when set, the script's stdout is a path to an image file; the
#                bot sends that image as a photo (template = caption)
#   timeout_sec  execution timeout (default 30s, max 300s)
#
# The commands file is edited only through the jq filter pipeline below, which
# guarantees:
#   * Values are JSON-escaped by jq (no shell/JSON injection possible).
#   * The result is schema-validated and checked for duplicates BEFORE it
#     replaces the existing file (atomic rename, chmod 600).
#   * A backup of the previous file is kept at commands.json.bak.
#
# Usage:
#   ./manage_commands.sh add <TEXT> --script <file.sh> \
#       [--template "<...>"] [--img] [--timeout 1-300]
#   ./manage_commands.sh edit <TEXT> [--rename <NEW_TEXT>] [--script <file.sh>]
#       [--template "..."] [--img|--no-img] [--timeout <1-300|0 resets>]
#   ./manage_commands.sh delete <TEXT>
#   ./manage_commands.sh list [--json]
#   ./manage_commands.sh help
#
# Environment:
#   COMMANDS_FILE   commands file (default: ../data/commands.json)
#   COMMANDS_DIR    directory holding the .sh scripts (default: ../commands)

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
COMMANDS_FILE="${COMMANDS_FILE:-$SCRIPT_DIR/../data/commands.json}"
COMMANDS_DIR="${COMMANDS_DIR:-$SCRIPT_DIR/../commands}"

fail() { echo "error: $*" >&2; exit 1; }

require_jq() {
  command -v jq >/dev/null 2>&1 || fail "jq is required (install with: apt install jq, brew install jq, dnf install jq)"
}

# ensure_file: create a minimal commands file when missing, and refuse to
# operate on garbage.
ensure_file() {
  if [[ ! -f "$COMMANDS_FILE" ]]; then
    mkdir -p "$(dirname "$COMMANDS_FILE")"
    printf '{\n  "version": 2,\n  "commands": []\n}\n' > "$COMMANDS_FILE"
    chmod 600 "$COMMANDS_FILE"
    echo "note: created empty $COMMANDS_FILE"
  fi
  validate_json "$COMMANDS_FILE" >/dev/null || fail "commands file is not valid: $COMMANDS_FILE"
}

# validate_json: schema + duplication checks; exit 0 when valid. Mirrors the
# Go runtime validation in internal/store.
validate_json() {
  jq -e '
    .version == 2
    and (.commands | type == "array")
    and ([.commands[].text] | length) == ([.commands[].text] | unique | length)
    and (all(.commands[];
        (.text | type == "string") and (.text | length) >= 1 and (.text | length) <= 64
        and (.script | type == "string") and (.script | length) >= 1
        and (.script | length) <= 512
        and (.script | endswith(".sh"))
        and (.script | startswith("/") | not)
        and ((.script | split("/")) | any(. == "..") | not)
        and (if (.timeout_sec // 30) < 1 or (.timeout_sec // 30) > 300 then false else true end)
        and (if (.template // "") == "" then true
             else (.template | type == "string") and ((.template | length) <= 4096)
               and (all([.template | scan("\\$\\{([A-Za-z_][A-Za-z0-9_]*)\\}")][]; .[0] == "output"))
             end)
        and (if (has("img")) then (.img | type == "boolean") else true end)))
  ' "$1" >/dev/null
}

# backup: keep a readable (for humans) previous copy.
backup() {
  cp -p "$COMMANDS_FILE" "$COMMANDS_FILE.bak"
  chmod 600 "$COMMANDS_FILE.bak"
}

# commit: run `jq "$@"` against the commands file and atomically install the
# result only after it passes validation.
commit() {
  local tmp
  tmp="$(mktemp "$(dirname "$COMMANDS_FILE")/.commands.XXXXXX.json")"
  trap 'rm -f "$tmp"' EXIT
  if ! jq "$@" "$COMMANDS_FILE" > "$tmp"; then
    fail "jq could not apply the change (file left untouched)"
  fi
  if ! validate_json "$tmp"; then
    fail "the resulting file failed validation (file left untouched)"
  fi
  chmod 600 "$tmp"
  mv -f "$tmp" "$COMMANDS_FILE"
  trap - EXIT
  echo "ok: $COMMANDS_FILE updated (backup: $COMMANDS_FILE.bak)"
}

validate_text() {
  local t="$1" byte
  [[ -n "$t" ]] || return 1
  [[ ${#t} -le 64 ]] || return 1
  # reject control characters (bytes < 32 or 127)
  for byte in $(printf '%s' "$t" | od -An -tu1); do
    if (( byte < 32 )) || (( byte == 127 )); then return 1; fi
  done
  return 0
}

validate_timeout() {
  # 0 means "use the default (30s)"; 1-300 sets an explicit timeout.
  [[ "$1" =~ ^[0-9]+$ ]] && (( $1 >= 0 && $1 <= 300 ))
}

# validate_script: script must be a relative .sh path inside the commands dir
# and the file must currently exist and be a regular file.
validate_script() {
  local s="$1"
  [[ -n "$s" ]] || fail "script path must not be empty"
  [[ ${#s} -le 512 ]] || fail "script path is too long"
  [[ "$s" != /* ]] || fail "script path must be relative to the commands dir ($COMMANDS_DIR)"
  [[ "$s" == *.sh ]] || fail "script path must end in .sh"
  if [[ "$s" == *"/../"* || "$s" == */.. || "$s" == ".." ]]; then
    fail "script path must not escape the commands dir"
  fi
  local abs="$COMMANDS_DIR/$s"
  [[ -f "$abs" ]] || fail "script file not found (expected $abs); create it first"
}

validate_template() {
  local t="$1"
  if [[ -n "$t" ]]; then
    [[ ${#t} -le 4096 ]] || fail "template is too long (max 4096 bytes)"
    # every ${...} reference must be ${output}
    local refs bad
    refs="$(printf '%s' "$t" | grep -oE '\$\{[A-Za-z_][A-Za-z0-9_]*\}' || true)"
    if [[ -n "$refs" ]]; then
      bad="$(printf '%s\n' "$refs" | grep -Fxv '${output}' || true)"
      [[ -z "$bad" ]] || fail "template may only reference \${output}, e.g. \"Disk usage: \${output}\""
    fi
  fi
}

usage() {
  cat <<'EOF'
Usage: ./manage_commands.sh <command> [options]

  add <TEXT> --script <file.sh> [--template "..."] [--img] [--timeout <1-300>]
      Register a button backed by a script in the commands directory.
      The script's stdout becomes the response.
        --template  optional response template; ${output} is replaced by the
                    script's output; \n and \t become newlines/tabs (for --img
                    commands it becomes the photo caption).
        --img       the script prints a path to an image file; the bot sends
                    that image as a photo (with the template as caption).

  edit <TEXT> [--rename <NEW_TEXT>] [--script <file.sh>] [--template "..."]
       [--img|--no-img] [--timeout <1-300|0 resets to default>]
      Change any fields of an existing command.

  delete <TEXT>          Remove a command.

  list [--json]          Show all commands (or the raw JSON).

  help                   Show this help.

Environment:
  COMMANDS_FILE   commands file (default data/commands.json).
  COMMANDS_DIR    scripts directory (default commands/).
EOF
}

add_cmd() {
  [[ $# -ge 1 ]] || fail "add requires a <TEXT>"
  local text="$1"; shift
  local script="" template="" timeout=0
  local timeout_set=false img=false

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --script)   script="${2:-}"; shift 2 ;;
      --template) template="${2:-}"; shift 2 ;;
      --img)      img=true; shift ;;
      --timeout)  timeout="${2:-}"; timeout_set=true; shift 2 ;;
      *) fail "add: unknown option '$1' (try 'help')" ;;
    esac
  done

  validate_text "$text" || fail "TEXT must be 1-64 characters without control characters"
  validate_script "$script"
  validate_template "$template"
  if [[ "$timeout_set" == "true" ]]; then
    validate_timeout "$timeout" || fail "add: --timeout must be 1-300"
  fi

  ensure_file
  backup
  commit --arg text "$text" --arg script "$script" --arg template "$template" \
         --argjson timeout "$timeout" --argjson img "$img" '
    if ([.commands[].text] | index($text)) then
      error("command already exists: " + $text)
    else
      .commands += [{text: $text, script: $script}
        + (if $template != "" then {template: $template} else {} end)
        + (if $img then {img: true} else {} end)
        + (if $timeout > 0 then {timeout_sec: $timeout} else {} end)]
    end
  '
}

list_cmd() {
  ensure_file
  if [[ "${1:-}" == "--json" ]]; then
    jq . "$COMMANDS_FILE"
    return
  fi
  local n
  n="$(jq '.commands | length' "$COMMANDS_FILE")"
  echo "commands ($n):"
  if (( n > 0 )); then
    jq -r '.commands[]
      | "\t\(.text)\t[\(if (.img // false) then "img" else "text" end)]"
        + "\t\(.script)"
        + "\t" + (if .template // "" | length > 0 then (.template // "") else "(no template)" end)
        + "\t(timeout: \(.timeout_sec // 30)s)"' "$COMMANDS_FILE" \
      | column -t -s $'\t' 2>/dev/null \
      || jq -r '.commands[] | "\t\(.text) [\(.script)]"' "$COMMANDS_FILE"
  fi
}

edit_cmd() {
  [[ $# -ge 1 ]] || fail "edit requires a <TEXT>"
  local text="$1"; shift
  local new_text="" new_script="" template="" timeout=0
  local set_script=false set_template=false set_timeout=false set_img=false
  local img=false

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --rename)   new_text="${2:-}"; shift 2 ;;
      --script)   new_script="${2:-}"; set_script=true; shift 2 ;;
      --template) template="${2:-}"; set_template=true; shift 2 ;;
      --img)      img=true; set_img=true; shift ;;
      --no-img)   img=false; set_img=true; shift ;;
      --timeout)  timeout="${2:-}"; set_timeout=true; shift 2 ;;
      *) fail "edit: unknown option '$1' (try 'help')" ;;
    esac
  done

  [[ -n "$new_text" ]] && { validate_text "$new_text" || fail "edit: --rename must be 1-64 characters"; }
  $set_script && validate_script "$new_script"
  $set_template && validate_template "$template"
  $set_timeout && { validate_timeout "$timeout" || fail "edit: --timeout must be 0-300 (0 resets to default)"; }

  ensure_file
  backup
  if $set_script; then sc_present="true"; else sc_present="false"; fi
  if $set_template; then tpl_present="true"; else tpl_present="false"; fi
  if $set_timeout; then tmo_present="true"; else tmo_present="false"; fi
  if $set_img; then im_present="true"; else im_present="false"; fi
  commit --arg text "$text" --arg new_text "$new_text" --arg new_script "$new_script" \
         --arg template "$template" --argjson set_template "$tpl_present" \
         --argjson timeout "$timeout" --argjson set_timeout "$tmo_present" \
         --argjson img "$img" --argjson set_img "$im_present" \
         --argjson set_script "$sc_present" '
    def idx: [.commands[].text] | index($text);
    if idx == null then
      error("command not found: " + $text)
    elif $new_text != "" and $new_text != .commands[idx].text and ([.commands[].text] | index($new_text)) != null then
      error("text already in use: " + $new_text)
    else
      .commands[idx] = (.commands[idx]
        | if ($new_text != "" and $new_text != .text) then .text = $new_text else . end
        | if $set_script then .script = $new_script else . end
        | if $set_template then .template = $template else . end
        | if $set_img then .img = $img else . end
        | if $set_timeout then (if $timeout > 0 then .timeout_sec = $timeout else del(.timeout_sec) end) else . end)
    end
  '
}

delete_cmd() {
  [[ $# -eq 1 ]] || fail "delete requires exactly one <TEXT>"
  local text="$1"
  ensure_file
  backup
  commit --arg text "$text" '
    if ([.commands[].text] | index($text)) == null then
      error("command not found: " + $text)
    else
      .commands |= map(select(.text != $text))
    end
  '
}

# --- dispatch ---------------------------------------------------------------
require_jq
cmd="${1:-}"
[[ $# -gt 0 ]] && shift

case "$cmd" in
  add)    add_cmd "$@" ;;
  list)   list_cmd "$@" ;;
  edit)   edit_cmd "$@" ;;
  delete) delete_cmd "$@" ;;
  help|-h|--help) usage ;;
  "")     usage; exit 1 ;;
  *)      fail "unknown command '$cmd' (try 'help')" ;;
esac