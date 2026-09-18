# telegram-pc-remote

A Telegram bot that remote-controls your PC through **pre-approved action
buttons**. You define a set of commands — each backed by a **shell script**
that you author in the `commands/` folder. When a whitelisted user presses a
button — or types its exact label — the bot runs the registered script and
sends its output back as the response. If the text does **not** match any
command, the bot answers with an error message.

Backend: **Go**. Command management: **bash** (`scripts/manage_commands.sh`).

---

## Features

| Feature | Where |
|---|---|
| Inline keyboard menu generated from configured commands | `service.go` (`showMenu`) |
| Button/callback text must match a stored command, otherwise an error is sent | `service.go` (`runCommand`) |
| Commands are user-authored `.sh` files in `commands/`, executed with `sh` | `internal/actions` |
| Output can be wrapped in a response template (`${output}`, `\n`, `\t`) | `internal/actions` / `internal/store` |
| Image commands: `--img` makes the printed path the photo (template = caption) | `internal/actions` |
| User **and** chat allowlist (both must match) | `internal/whitelist` |
| Rate limiting: at most 1 message per chat per second; extras are ignored | `internal/ratelimit` |
| Add / list / edit / delete commands from a shell script | `scripts/manage_commands.sh` |
| Live reload of commands & whitelist (every 5 s or on `SIGHUP`) | `service.go` (`refreshLoop`) |

---

## Quick start

```bash
# 1. Configure
cp .env.example .env          # set TELEGRAM_BOT_TOKEN (from @BotFather)
chmod 600 .env

# 2. Whitelist yourself (numeric IDs — get them from @userinfobot)
#    Edit data/whitelist.json and add your user ID and chat ID.

# 3. Author scripts and register buttons
scripts/manage_commands.sh add "System Status" --script status.sh --timeout 15
scripts/manage_commands.sh add "Screenshot"    --script screenshot.sh --img \
                            --template '📸 Screenshot taken'
scripts/manage_commands.sh add "Hello"         --script hello.sh

# 4. Run
make run      # or: TELEGRAM_BOT_TOKEN=... ./bin/telegram-pc-remote
```

Then open the chat with your bot, send `/menu` (or `/start`) and press a
button. Pressing a button whose text has no matching command produces:

```
⚠️ Unknown command: "xyz". Send /menu to see the available buttons.
```

---

## How commands work

A command is a **button label** (`text`) plus a **script file** (`script`,
relative to the commands directory — `commands/` by default). The script:

- is a plain shell script you create yourself (e.g. `commands/status.sh`);
- runs with `sh <script>` in the **commands directory** as its working
  directory, so relative paths inside scripts behave predictably;
- has its **stdout** (stdout+stderr) captured and returned to the chat;
- output is trimmed and capped at ~3.5 KB.

```json
{
  "text": "System Status",
  "script": "status.sh",
  "timeout_sec": 15
}
```

### Response templates

With `--template '...'` you can wrap the script's output. The placeholder
`${output}` is replaced by the script's output, and `\n` / `\t` become real
newlines/tabs (handy for multi-line replies on a single command line):

```bash
scripts/manage_commands.sh add "System Report" --script report.sh \
  --template '📊 System report:\n${output}' --timeout 20
```

### Image commands (`--img`)

When a command has the `--img` flag, its script's output is treated as a
**path to an image file** (absolute, or relative to the commands directory).
The bot reads that file and sends it as a **photo**. The `--template` text, if
present, becomes the photo's caption:

```bash
scripts/manage_commands.sh add "Screenshot" --script screenshot.sh --img \
  --template '📸 Screenshot taken'
```

The image path is validated before sending: it must be a regular file, stay
under the 9 MiB cap, and its magic bytes must identify a PNG/JPEG/GIF/WebP/BMP
— arbitrary binaries are never sent to the chat.

### Management script

```bash
# add
./scripts/manage_commands.sh add "Lock PC" --script lock.sh --timeout 10
./scripts/manage_commands.sh add "Uptime"  --script status.sh
./scripts/manage_commands.sh add "Screenshot" --script screenshot.sh --img --template '📸 Shot'

# list
./scripts/manage_commands.sh list
./scripts/manage_commands.sh list --json

# edit (change any subset; --img/--no-img flip the flag, --timeout 0 resets)
./scripts/manage_commands.sh edit "Lock PC" --rename "Lock Screen" --timeout 20
./scripts/manage_commands.sh edit "Uptime" --script uptime.sh --template 'Uptime: ${output}'

# delete
./scripts/manage_commands.sh delete "Hello"
```

**Schema of one command** (`data/commands.json`, version 2):

```json
{
  "text": "System Status",
  "script": "status.sh",
  "template": "Uptime: ${output}",
  "img": false,
  "timeout_sec": 15
}
```

| Field | Meaning |
|---|---|
| `text` | button label AND exact text to match (1–64 chars) |
| `script` | `.sh` file to run, relative to the commands directory |
| `template` | optional; `${output}` = script output, `\n`/`\t` = newline/tab (caption for `--img` commands) |
| `img` | optional; when true the script's output is a path to an image sent as a photo |
| `timeout_sec` | optional, 1–300 (default 30) |

Templates are validated at load time: they may only reference `${output}`,
so there is no injection vector through template text.

The script:

- **escapes everything through `jq`** — quotes, backticks and `$` in values
  cannot break out of the JSON or the shell;
- **validates the result before installing it** (schema, duplicate texts,
  script containment and existence, template placeholders, timeout bounds)
  and writes atomically (`tmp` + `rename`);
- keeps a backup at `data/commands.json.bak`;
- refuses to run without `jq` (a one-line message explains how to install it).

> Built-in navigation commands are `/start`, `/menu`, `/commands` and
> `/help`. They are not stored in the command list, and they take precedence
> over any command named with a leading `/`.

---

## Security model

This bot is a remote-access tool, so it is deliberately paranoid. The threat
model: *anyone on the internet can send messages to your bot*; the bot must
treat every update as hostile until proven trustworthy.

### 1. Allowlist (fail closed)

- `data/whitelist.json` holds numeric **user IDs** and **chat IDs**.
- An update is processed **only if the sender AND the chat are both
  allowlisted** (`internal/whitelist`).
- Missing, corrupt, or empty whitelist = **deny everything**. The bot
  refuses to start on a missing/corrupt file, and starts deny-all on an empty
  one.
- Unauthorized updates are dropped **silently** — no reply, no callback ack —
  so the bot never confirms its existence or reveals its command set to
  strangers.
- A failed reload keeps the previous valid allowlist in memory (a bad edit
  can neither lock you out nor let anyone in).

### 2. Rate limiting

- One token bucket **per chat**, capacity exactly 1, refill 1/sec
  (`internal/ratelimit`). More than one message per second from the same
  chat → the extras are ignored, no burst is ever allowed.
- Checks run *after* the allowlist and *before* any command executes.

### 3. Commands are pre-approved — never free-form

- Users can only trigger commands the **operator** has defined. There is no
  path for a user to inject text into a shell command.
- Script paths come from the operator-approved store and are launched in
  `argv` form (`sh <path>`), never through `sh -c` with unfiltered input — so
  there is no shell-injection surface. Paths must be relative, stay inside
  the commands directory, and point at an existing regular `.sh` file
  (checked again at execution time as defense in depth).
- The `.sh` script is the only intended way to change commands, and it
  validates before writing.

### 4. Shell execution hardening

Applies to every command script:

- Every script gets a timeout (`timeout_sec`, hard-capped at 300 s) plus a
  global cap on command duration (`BOT_MAX_COMMAND_SECONDS`).
- The script runs in its **own process group** (`setsid`); on timeout the
  whole subtree is killed with `SIGKILL`, not just the `sh` parent.
- The environment is stripped to a minimal `PATH` — no `HOME`, no secrets,
  no inherited variables.
- The working directory is the commands directory (so relative paths inside
  scripts and image replies work) and output is truncated (~3.5 KB) so a chat
  cannot be flooded past Telegram's message limit.
- Concurrency is bounded (`BOT_MAX_CONCURRENT`); panics are caught so a bad
  update cannot take the bot down.

**Image validation** — `--img` replies are read from a path printed by the
script, then sanity-checked: regular file, ≤ 9 MiB, and magic bytes must match
a known image format. Nothing else is ever sent to the chat.

**Template validation** — templates may only reference `${output}`; an
unknown `${name}` placeholder is a load-time error.

### 5. Secrets & files

- The token comes from `TELEGRAM_BOT_TOKEN` (env or `.env`, `chmod 600`,
  git-ignored) and is **never logged** — only the bot username is printed.
- `commands.json` / `whitelist.json` are written with `0600` (script and
  `safeio`); all writes are atomic (temp file → fsync → rename), so the bot
  and the script can never observe a torn file.
- Logs contain only IDs and command names — never message bodies or shell
  output.

### 6. Runtime reload

- Commands and the whitelist are checked every `BOT_RELOAD_SECONDS` (5 s)
  and re-read when the file changes (mtime/size), or immediately on
  `SIGHUP` — no restart needed to add users or buttons. Script files are read
  fresh on every press, so editing a script needs no reload at all.

---

## Configuration

| Variable | Default | Meaning |
|---|---|---|
| `TELEGRAM_BOT_TOKEN` | — (required) | Bot token from @BotFather |
| `DATA_DIR` | `data` | Directory holding `commands.json` + `whitelist.json` |
| `COMMANDS_DIR` | `commands` | Directory holding the user-authored `.sh` scripts |
| `BOT_RELOAD_SECONDS` | `5` | Config-file re-check interval |
| `BOT_MAX_CONCURRENT` | `4` | Max updates processed in parallel |
| `BOT_MAX_COMMAND_SECONDS` | `600` | Hard cap per command execution |

An optional `.env` file is read for local convenience; real environment
variables always win. For systemd, prefer `EnvironmentFile=`.

---

## Example systemd unit

```ini
[Unit]
Description=Telegram PC Remote bot
After=network-online.target

[Service]
User=pcadmin
WorkingDirectory=/opt/telegram-pc-remote
EnvironmentFile=/etc/telegram-pc-remote.env
ExecStart=/opt/telegram-pc-remote/bin/telegram-pc-remote
ExecReload=/bin/kill -HUP $MAINPID
Restart=on-failure
NoNewPrivileges=true
PrivateTmp=true

[Install]
WantedBy=multi-user.target
```

`kill -HUP` reloads commands and the whitelist from disk.

---

## Project layout

```
main.go                     entry point, bot wiring
service.go                  update pipeline: allowlist → rate limit → dispatch
commands/                   your .sh scripts (one per command button)
internal/
  actions/                  hardened script execution + result/image handling
  config/                   env/config loading
  ratelimit/                per-chat 1 msg/sec token bucket
  safeio/                   atomic 0600 file writes
  store/                    validated command store (lookup, list, menu data)
  whitelist/                user+chat allowlist, fail closed
data/
  commands.json             command definitions
  whitelist.json            user/chat allowlist
scripts/
  manage_commands.sh        add / list / edit / delete commands
```

## Development

```bash
make test       # unit tests (store, whitelist, ratelimit, actions, …)
make vet        # go vet
make build      # ./bin/telegram-pc-remote
```

To try the command script without touching the real data file:

```bash
COMMANDS_FILE=/tmp/demo.json ./scripts/manage_commands.sh add "Ping" --script ping.sh --timeout 20
COMMANDS_FILE=/tmp/demo.json ./scripts/manage_commands.sh list
```

(remember `COMMANDS_DIR` is `commands/` relative to the script; point `ping.sh`
to a file that exists, e.g. `COMMANDS_DIR=/tmp ./scripts/manage_commands.sh add "Ping" --script my.sh`
with `my.sh` in `/tmp`.)