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
    and ([.commands[] | select(.menu != null) | .menu.id] | length) ==
        ([.commands[] | select(.menu != null) | .menu.id] | unique | length)
    and ((([.commands[] | select(.menu != null) | .menu.id]) as $ids
      | all([.commands[] | select(.menu != null) | .menu.options[]? | .menu_id? // empty][];
            $ids | index(.) != null)))
    and (all(.commands[];
        (.text | type == "string") and (.text | length) >= 1 and (.text | length) <= 64
        and (((((.script // "") != "") and ((.menu // null) == null))
              or (((.script // "") == "") and ((.menu // null) != null))))
        and (((.script // "") == "")
             or ((.script | type == "string") and (.script | length) <= 512
                 and (.script | endswith(".sh")) and (.script | startswith("/") | not)
                 and (((.script | split("/")) | any(. == "..")) | not)))
        and (((.menu // null) == null)
             or ((.menu | type == "object")
                 and (.menu.id | type == "string") and (.menu.id | length) >= 1
                 and (.menu.id | length) <= 32 and (.menu.id | test("^[a-z0-9-]+$"))
                 and ((.menu.prompt // "") | type == "string")
                 and (((.menu.prompt // "") | length) <= 4096)
                 and (((.template // "") == "") and (has("img") | not))
                 and (.menu.options | type == "array")
                 and ([.menu.options[].label] | length) == ([.menu.options[].label] | unique | length)
                 and (all(.menu.options[];
                     (.label | type == "string") and (.label | length) >= 1 and (.label | length) <= 64
                     and ((.value == null) or ((.value | type == "string") and (.value | length) >= 1 and (.value | length) <= 64))
                     and (if ((.menu_id // "") != "") then
                           (.menu_id | type == "string") and (.menu_id | length) <= 32
                           and (.menu_id | test("^[a-z0-9-]+$"))
                           and ((.script // "") == "") and ((.value // null) == null)
                         else
                           (((.script // "") == "")
                            or ((.script | type == "string") and (.script | length) <= 512
                                and (.script | endswith(".sh")) and (.script | startswith("/") | not)
                                and (((.script | split("/")) | any(. == "..")) | not)))
                         end)))
                 and (((((.menu.script // "") != "")
                        and (.menu.script | type == "string") and ((.menu.script | length) <= 512)
                        and (.menu.script | endswith(".sh")) and (.menu.script | startswith("/") | not)
                        and (((.menu.script | split("/")) | any(. == "..")) | not))
                       or (((.menu.script // "") == "")
                           and (all(.menu.options[]; (((.script // "") != "") or ((.menu_id // "") != "")))))))))
        and ((.timeout_sec // 30) >= 1) and ((.timeout_sec // 30) <= 300)
        and ((.template // "") == ""
             or ((.template | type == "string") and ((.template | length) <= 4096)
                 and (all([.template | scan("\\$\\{([A-Za-z_][A-Za-z0-9_]*)\\}")][]; .[0] == "output"))))
        and ((has("hidden") | not) or (.hidden | type == "boolean"))
        and ((has("img") | not) or (.img | type == "boolean"))))
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

validate_menu_id() {
  [[ "$1" =~ ^[a-z0-9-]{1,32}$ ]]
}

validate_prompt() {
  [[ ${#1} -le 4096 ]]
}

# gen_menu_id: derive a short stable id from the button text and make it
# unique among the menus already stored in the commands file.
gen_menu_id() {
  local base="$1" candidate i=0
  base="$(printf '%s' "$base" | tr '[:upper:]' '[:lower:]' | tr -cs 'a-z0-9' '-')"
  base="${base:0:20}"
  base="${base#-}"
  [[ -n "$base" ]] || base="menu"
  candidate="$base"
  while jq -e --arg id "$candidate" \
    '[.commands[] | select(.menu != null) | .menu.id] | index($id) != null' \
    "$COMMANDS_FILE" >/dev/null 2>&1; do
    i=$((i+1))
    candidate="${base}-${i}"
  done
  printf '%s' "$candidate"
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

  addmenu <TEXT> [--prompt "..."] [--script <shared-handler.sh>]
          [--menu-id <id>] [--hidden] [--timeout <1-300>]
      Register a button that answers with chooseable options (also buttons).
      The options are added with 'addopt'. Each option either runs its own
      script, falls back to the menu's shared handler — which receives the
      option's value as $1 (and TPR_OPTION; the label as TPR_OPTION_LABEL) —
      or opens another menu as a nested submenu.
        --prompt    text shown above the option buttons (default: "Select an
                    option: "). \n and \t become newlines/tabs.
        --menu-id   short id used in the option buttons' callback data
                    (default: derived from the text, e.g. "change-workspace").
        --hidden    hide this command from the /menu keyboard — use it for
                    menus that exist only as submenu targets.

  addopt <TEXT> --label "<option>" [--value <v>] [--script <file.sh>]
        [--menu <menu-id-or-TEXT>]
      Add an option button to menu <TEXT>.
        --label    button text (this is also the value passed to the handler
                   unless --value overrides it).
        --value    optional value passed to the handler script instead of the
                   label (as $1 / TPR_OPTION).
        --script   optional per-option script; without it the menu's shared
                   handler runs.
        --menu     make this option open ANOTHER menu (a nested submenu),
                   given its menu id or the menu command's text. Cannot be
                   combined with --script/--value.

  delopt <TEXT> --label "<option>"   Remove an option button from menu <TEXT>.

  opts <TEXT> [--json]               List a menu's options.

  edit <TEXT> [--rename <NEW_TEXT>] [--script <file.sh>] [--template "..."]
       [--img|--no-img] [--timeout <1-300|0 resets to default>]
       [--menu-prompt "..."] [--menu-script <file.sh>]
       [--hidden|--no-hidden]
      Change any fields of an existing command (menu fields apply to menus).

After ANY script command ends (a top-level script button like report.sh, or
a menu option), the bot re-sends the GENERAL menu (the /menu keyboard with
all non-hidden commands), so you're back at the top level. Descending into a
nested menu sends nothing extra; the general menu only comes back after the
final script of the flow has replied.

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
      | "\t\(.text)\(if (.hidden // false) then " (hidden)" else "" end)\t" + (if (.menu // null) != null
          then "[menu]\tid: \(.menu.id)\thandler: \(.menu.script // "(none)")\toptions: \(.menu.options | length)"
          else "[\(if (.img // false) then "img" else "text" end)]\tscript: \(.script)\ttemplate: \(if .template // "" | length > 0 then (.template // "") else "(no template)" end)" end)
          + "\t(timeout: \(.timeout_sec // 30)s)"' "$COMMANDS_FILE" \
      | column -t -s $'\t' 2>/dev/null \
      || jq -r '.commands[] | "\t\(.text) [\(.script // .menu.id)]"' "$COMMANDS_FILE"
  fi
}

edit_cmd() {
  [[ $# -ge 1 ]] || fail "edit requires a <TEXT>"
  local text="$1"; shift
  local new_text="" new_script="" template="" timeout=0
  local set_script=false set_template=false set_timeout=false set_img=false
  local menu_prompt="" menu_script=""
  local set_menu_prompt=false set_menu_script=false
  local img=false hidden=false set_hidden=false

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --rename)   new_text="${2:-}"; shift 2 ;;
      --script)   new_script="${2:-}"; set_script=true; shift 2 ;;
      --template) template="${2:-}"; set_template=true; shift 2 ;;
      --img)      img=true; set_img=true; shift ;;
      --no-img)   img=false; set_img=true; shift ;;
      --timeout)  timeout="${2:-}"; set_timeout=true; shift 2 ;;
      --menu-prompt)  menu_prompt="${2:-}"; set_menu_prompt=true; shift 2 ;;
      --menu-script)  menu_script="${2:-}"; set_menu_script=true; shift 2 ;;
      --hidden)   hidden=true; set_hidden=true; shift ;;
      --no-hidden) hidden=false; set_hidden=true; shift ;;
      *) fail "edit: unknown option '$1' (try 'help')" ;;
    esac
  done

  [[ -n "$new_text" ]] && { validate_text "$new_text" || fail "edit: --rename must be 1-64 characters"; }
  $set_script && validate_script "$new_script"
  $set_template && validate_template "$template"
  $set_timeout && { validate_timeout "$timeout" || fail "edit: --timeout must be 0-300 (0 resets to default)"; }
  $set_menu_prompt && validate_prompt "$menu_prompt"
  $set_menu_script && [[ -n "$menu_script" ]] && validate_script "$menu_script"

  ensure_file
  backup
  if $set_script; then sc_present="true"; else sc_present="false"; fi
  if $set_template; then tpl_present="true"; else tpl_present="false"; fi
  if $set_timeout; then tmo_present="true"; else tmo_present="false"; fi
  if $set_img; then im_present="true"; else im_present="false"; fi
  if $set_menu_prompt; then mp_present="true"; else mp_present="false"; fi
  if $set_menu_script; then ms_present="true"; else ms_present="false"; fi
  if $set_hidden; then hd_present="true"; else hd_present="false"; fi
  commit --arg text "$text" --arg new_text "$new_text" --arg new_script "$new_script" \
         --arg template "$template" --argjson set_template "$tpl_present" \
         --argjson timeout "$timeout" --argjson set_timeout "$tmo_present" \
         --argjson img "$img" --argjson set_img "$im_present" \
         --argjson set_script "$sc_present" \
         --arg menu_prompt "$menu_prompt" --argjson set_menu_prompt "$mp_present" \
         --arg menu_script "$menu_script" --argjson set_menu_script "$ms_present" \
         --argjson hidden "$hidden" --argjson set_hidden "$hd_present" '
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
        | if $set_timeout then (if $timeout > 0 then .timeout_sec = $timeout else del(.timeout_sec) end) else . end
        | if $set_menu_prompt then .menu.prompt = $menu_prompt else . end
        | if $set_menu_script then (if $menu_script != "" then .menu.script = $menu_script else del(.menu.script) end) else . end
        | if $set_hidden then (if $hidden then .hidden = true else del(.hidden) end) else . end)
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

addmenu_cmd() {
  [[ $# -ge 1 ]] || fail "addmenu requires a <TEXT>"
  local text="$1"; shift
  local prompt="" script="" menu_id="" timeout=0 timeout_set=false hidden=false

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --prompt)   prompt="${2:-}"; shift 2 ;;
      --script)   script="${2:-}"; shift 2 ;;
      --menu-id)  menu_id="${2:-}"; shift 2 ;;
      --timeout)  timeout="${2:-}"; timeout_set=true; shift 2 ;;
      --hidden)   hidden=true; shift ;;
      *) fail "addmenu: unknown option '$1' (try 'help')" ;;
    esac
  done

  validate_text "$text" || fail "TEXT must be 1-64 characters without control characters"
  validate_prompt "$prompt" || fail "addmenu: --prompt is too long (max 4096 bytes)"
  [[ -n "$script" ]] && validate_script "$script"
  if [[ "$timeout_set" == "true" ]]; then
    validate_timeout "$timeout" || fail "addmenu: --timeout must be 1-300"
  fi

  ensure_file
  [[ -z "$menu_id" ]] && menu_id="$(gen_menu_id "$text")"
  validate_menu_id "$menu_id" || fail "addmenu: --menu-id must match ^[a-z0-9-]{1,32}$"
  [[ -n "$prompt" ]] || prompt="Select an option:"
  backup
  commit --arg text "$text" --arg menu_id "$menu_id" --arg prompt "$prompt" \
         --arg script "$script" --argjson timeout "$timeout" --argjson hidden "$hidden" '
    if ([.commands[].text] | index($text)) then
      error("command already exists: " + $text)
    elif ([.commands[] | select(.menu != null) | .menu.id] | index($menu_id)) != null then
      error("menu id already in use: " + $menu_id)
    else
      .commands += [{text: $text,
                     menu: {id: $menu_id, prompt: $prompt, options: []}
                       + (if $script != "" then {script: $script} else {} end)}
        + (if $timeout > 0 then {timeout_sec: $timeout} else {} end)
        + (if $hidden then {hidden: true} else {} end)]
    end
  '
  echo "menu id: $menu_id — add options with: $0 addopt \"$text\" --label \"<option>\""
}

addopt_cmd() {
  [[ $# -ge 1 ]] || fail "addopt requires a <TEXT>"
  local text="$1"; shift
  local label="" value="" script="" target=""
  local by_menu=false

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --label)  label="${2:-}"; shift 2 ;;
      --value)  value="${2:-}"; shift 2 ;;
      --script) script="${2:-}"; shift 2 ;;
      --menu)   target="${2:-}"; shift 2 ;;
      *) fail "addopt: unknown option '$1' (try 'help')" ;;
    esac
  done
  [[ -n "$label" ]] || fail "addopt requires --label"
  validate_text "$label" || fail "addopt: --label must be 1-64 characters without control characters"
  if [[ -n "$value" ]]; then
    validate_text "$value" || fail "addopt: --value must be 1-64 characters without control characters"
  fi
  [[ -n "$script" ]] && validate_script "$script"
  if [[ -n "$target" ]]; then
    # A menu option opens another menu instead of running a script.
    if [[ -n "$script" || -n "$value" ]]; then
      fail "addopt: --menu (open another menu) cannot be combined with --script or --value"
    fi
    by_menu=true
  fi

  ensure_file
  local menu_id=""
  if $by_menu; then
    # Resolve the target (its id, or the menu command's text) to a menu id.
    # Done on the current file, so a later commit cannot reference a stale id.
    menu_id="$(jq -r --arg target "$target" '
      if ([.commands[] | select(.menu != null) | .menu.id] | index($target)) != null then $target
      else ([.commands[] | select(.text == $target and .menu != null) | .menu.id] | first // "") end' "$COMMANDS_FILE")"
    [[ -n "$menu_id" ]] || fail "addopt: no menu found for target '$target' (id or menu command text)"
  fi
  backup
  if $by_menu; then
    commit --arg text "$text" --arg label "$label" --arg menu_id "$menu_id" '
      def idx: [.commands[].text] | index($text);
      if idx == null then
        error("command not found: " + $text)
      elif ((.commands[idx].menu // null) == null) then
        error("not a menu command: " + $text)
      elif ([.commands[idx].menu.options[].label] | index($label)) != null then
        error("option already exists: " + $label)
      else
        .commands[idx].menu.options += [{label: $label, menu_id: $menu_id}]
      end
    '
  else
    commit --arg text "$text" --arg label "$label" --arg value "$value" --arg script "$script" '
      def idx: [.commands[].text] | index($text);
      if idx == null then
        error("command not found: " + $text)
      elif ((.commands[idx].menu // null) == null) then
        error("not a menu command: " + $text)
      elif ([.commands[idx].menu.options[].label] | index($label)) != null then
        error("option already exists: " + $label)
      else
        .commands[idx].menu.options +=
          [{label: $label}
           + (if $value != "" then {value: $value} else {} end)
           + (if $script != "" then {script: $script} else {} end)]
      end
    '
  fi
}

delopt_cmd() {
  [[ $# -ge 1 ]] || fail "delopt requires a <TEXT>"
  local text="$1"; shift
  local label=""
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --label) label="${2:-}"; shift 2 ;;
      *) fail "delopt: unknown option '$1' (try 'help')" ;;
    esac
  done
  [[ -n "$label" ]] || fail "delopt requires --label"

  ensure_file
  backup
  commit --arg text "$text" --arg label "$label" '
    def idx: [.commands[].text] | index($text);
    if idx == null then
      error("command not found: " + $text)
    elif ((.commands[idx].menu // null) == null) then
      error("not a menu command: " + $text)
    elif ([.commands[idx].menu.options[].label] | index($label)) == null then
      error("option not found: " + $label)
    else
      .commands[idx].menu.options |= map(select(.label != $label))
    end
  '
}

opts_cmd() {
  [[ $# -ge 1 ]] || fail "opts requires a <TEXT>"
  local text="$1"; shift
  local json_only=false
  [[ "${1:-}" == "--json" ]] && json_only=true
  ensure_file
  if $json_only; then
    jq -c --arg text "$text" '
      def idx: [.commands[].text] | index($text);
      if idx == null then error("command not found: " + $text)
      else .commands[idx].menu end' "$COMMANDS_FILE"
  else
    jq -r --arg text "$text" '
      def idx: [.commands[].text] | index($text);
      if idx == null then error("command not found: " + $text)
      else
        "menu \"\(.commands[idx].text)\"  id: \(.commands[idx].menu.id)  handler: \(.commands[idx].menu.script // "(none)")",
        "prompt: \(.commands[idx].menu.prompt // "(default)")",
        (.commands[idx].menu.options[] |
          "  [\(.label)]  value: \(.value // "(=label)")  \(if (.menu_id // "") != "" then "opens menu: \(.menu_id)" else "script: \(.script // "(menu handler)")" end)")
      end' "$COMMANDS_FILE"
  fi
}

# --- dispatch ---------------------------------------------------------------
require_jq
cmd="${1:-}"
[[ $# -gt 0 ]] && shift

case "$cmd" in
  add)     add_cmd "$@" ;;
  list)    list_cmd "$@" ;;
  edit)    edit_cmd "$@" ;;
  delete)  delete_cmd "$@" ;;
  addmenu) addmenu_cmd "$@" ;;
  addopt)  addopt_cmd "$@" ;;
  delopt)  delopt_cmd "$@" ;;
  opts)    opts_cmd "$@" ;;
  help|-h|--help) usage ;;
  "")     usage; exit 1 ;;
  *)      fail "unknown command '$cmd' (try 'help')" ;;
esac