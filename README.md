# POSTERM — the lab wall

English · [Français](README.fr.md)

**An agent job is a terminal that runs on its own. That's it. That's the whole idea.**

**POSTERM** = post-it + terminal. A wall of post-its, and every post-it is a real terminal.

No agent framework. No job queue. No database. No orchestration layer that
"manages" your agents and gets in the way. `claude`, `codex`, `crush` or plain
`bash` run in tmux, because tmux already solved persistence decades ago, and
POSTERM puts all of them on one wall you can read in half a second, from your
desk or your phone.

![The wall: one post-it per lab, colored by state](docs/img/wall.png)

Built by the person who uses it, every day, morning, noon and night, since the
first commit. It is not a demo that grew up; it's the tool, and it gets fixed
the day it gets in the way. The agent that wrote this README was itself running
in a POSTERM lab, on that wall.

## The problem

Run more than three coding agents at once and you know how it goes: a dozen
terminal tabs, and the one that has been sitting on a `[y/N]` for twenty minutes
is, of course, the one you didn't look at.

So: every job is a **lab**, a directory plus a command. Every lab is a post-it.
The color tells you whether it needs you. Nothing else on the screen matters.

| color  | state      | what it means                                          |
| ------ | ---------- | ------------------------------------------------------ |
| red    | `running`  | the screen is moving. Leave it alone.                  |
| yellow | `question` | silent and stuck on a prompt or a dialog. Your turn.   |
| green  | `idle`     | silent, nothing asked. Done, or waiting for orders.    |
| grey   | `dead`     | no process left. The exit code is on the post-it.      |

## What you actually get

**The wall**
- Every post-it shows the live tail of its screen, or the exact question the
  agent asked. The tab title and the app badge count the yellow ones.
- The state counters are filters. The search box greps name, dir and command.
  Labs are grouped by machine.
- **Broadcast**: tick some post-its, type one line, every one of them gets it,
  Enter included. `continue`. `run the tests`. `commit`. Done.
- **Log**: every question, every lab that settles after real work, every
  death, with a counter of what happened while you weren't looking.

**A lab, opened**

![An open lab: the agent's terminal, the rail, and the lab's own bash](docs/img/lab.png)

- Click a post-it: you're in the **real terminal**, xterm.js on tmux. Not a
  log viewer, not a chat window. Close the tab, kill the browser, restart the
  hub: the job doesn't care.
- **Select text, get a menu where you let go**: copy, or save as `.md`. A plain
  drag selects in tmux (it hands the text over with OSC 52), Shift+drag selects
  in xterm. Click anywhere else and nothing is copied.
- The **rail** lists every lab in its color. Drag to reorder; the wall and the
  keyboard follow.
- `Ctrl+Tab` / `Ctrl+Shift+Tab` (or `Alt+PageDown` / `Alt+PageUp`) walk the
  labs, `Alt+Q` jumps to the next question, `Alt+M` goes back to the wall.
- **A bash in every lab**, at the bottom, in the lab's dir, for the `git diff`
  you want while the agent works. On/off per lab, drag to resize, and it
  survives exactly like the lab does.

**Drop files on it**

![Dropping on the bash uploads into its current directory](docs/img/drop.png)

- Drag files *or whole directories* onto the bash. They land in **its cwd**,
  wherever you `cd`'d. The target is shown before you let go.
- Directories are zipped in the browser (deflate; media and archives stored
  as is, recompressing JPEGs is a waste of everyone's time), streamed once,
  unpacked server-side. A dropped `.zip` is unpacked into `<name>/` and not kept.
- Nothing gets overwritten without asking. `Esc` or ✕ aborts at any point, and
  an aborted or failed upload rolls back: nothing written. `Alt` + drop sends
  raw, no zip, no unpacking.

**Away from the desk**
- Install it as an app (PWA). **Web Push** fires when a lab asks something or
  settles; where the browser allows it, the notification has a **Reply** field
  that types straight into the lab. No push for a lab you're already watching.
- On a phone, a key bar gives you `esc`, `tab`, `^C`, the arrows and Enter.

**Exact state, when guessing isn't good enough**

The colors come from a heuristic (below). If you want them exact, have the lab
say so. Wire it into your agent's hooks:

```sh
posterm state question "Delete build/?"   # yellow, with the text on the post-it
posterm state idle                        # green
```

No network, no token: it writes a file in `$POSTERM_DATA/etat/`. The filesystem
is the IPC. `POSTERM_LAB` and `POSTERM_DATA` are set in every lab. The declared
state holds until the screen moves again. (`posterm etat question|pose` is the
French spelling and still works.)

## A day on the wall

- **Morning.** Open the wall. Whatever ran overnight is green, yellow or grey,
  and the log tells you what happened while you slept. Yellow first.
- **Day.** Five, ten labs at once, on one machine or several. You don't babysit
  tabs anymore: your eyes go where the color is. Broadcast `run the tests` to
  everything green, review the diff in the lab's bash, drop the spec file on it.
- **Away.** The phone buzzes: a lab has a question. Answer from the
  notification, without unlocking a laptop.
- **Night.** Start the long ones and close the browser. The labs live in tmux
  on the machine, not in your tab; they'll be there tomorrow.

## One binary, four faces

```
posterm                       the hub: web wall + this machine's labs
posterm join [hub-url]        a spoke: publish this machine's labs to the hub
posterm lab <dir> [cmd…]      spawn a lab here, from the shell
posterm state <question|idle> [text…]   from inside a lab: say where it is
```

## Getting started

```sh
curl -fsSL https://posterm.sh/install | sh
```

Linux: the static binary goes to `~/.local/bin` (`/usr/local/bin` as root), and
it tells you if tmux is missing. macOS: it runs the Mac package below. Every
download is checked against the release's `SHA256SUMS`. It's 100-odd lines of
`sh`: read it first, [it's short on purpose](https://posterm.sh/install).

From source, you need Go and `tmux` >= 3.1. That's the dependency list.

```sh
cp .env.example .env   # then edit it: title, language, address, data dir
bin/build.sh           # gofmt + vet, static build, backs up the old binary
./posterm                # http://127.0.0.1:8206
```

First start generates an access token, prints it and keeps it in
`$POSTERM_DATA/token`. Open the page, paste the token, hit **+ lab**: pick a dir
(with completion) and a command (recent ones first).

`bin/build.sh` keeps timestamped backups in `bin/backup/` (7 days) and ends with
`systemctl restart posterm`. Not running it as a service? Delete that line.

### On a Mac, no compiler involved

`macos/build.sh` makes `macos/dist/POSTERM-macos.zip`. Whoever gets it:

1. unzips it and double-clicks `install.command`: it fetches tmux (Homebrew),
   installs `~/.local/bin/posterm`, starts the hub at login (LaunchAgent) and
   opens Safari with the token already in the clipboard;
2. pastes the token;
3. hits **File → Add to Dock**. That's the app: own window, own icon, own
   notifications.

Labs survive updates and closed windows, `uninstall.command` asks before
touching anything that matters. Walkthrough and gotchas (macOS guards
Desktop/Documents): [`macos/README.md`](macos/README.md).

## More machines

Same binary, everywhere:

```sh
POSTERM_TOKEN=<hub token> ./posterm join https://hub.example.org
```

The spoke dials **out**: one WebSocket to the hub. No port to open, NAT and
home routers are not your problem. Inside the tunnel (yamux) the hub speaks
plain HTTP to the spoke, because every machine exposes the exact same tiny API
and the hub is nothing but a reverse proxy to `/m/<machine>/…`. Spawn a spoke's
labs from the wall (pick the machine) or right there with `posterm lab`.

![Architecture: one hub, spokes dialing out, tmux everywhere](docs/x/x-04-arch.png)

## How a lab turns yellow

Once a second, the engine looks at tmux:

1. **the screen moved** less than `POSTERM_QUIET` seconds ago → *running*;
2. otherwise, if the **cursor line** looks like a prompt (`[y/N]`, `password`,
   ends with `?`…) or the screen shows a known **dialog** (regex
   `POSTERM_QUESTION`) → *question*;
3. otherwise → *idle*.

Yes, it's a heuristic. A command that stays quiet for a long time looks idle,
and the built-in dialogs (crush, Claude Code) may need tuning through
`POSTERM_QUESTION`. If that bothers you, use `posterm state` from the hooks and
stop guessing.

## Configuration

Environment variables, or a `.env` in the working directory (commented
template: `.env.example`; the real environment wins). Everything that belongs
to *one* installation (name, language, domain, token, machine names, paths)
lives there. Never in the code, never in this repo.

| variable         | default          | what it does                                                  |
| ---------------- | ---------------- | ------------------------------------------------------------- |
| `POSTERM_TITLE`    | `POSTERM`       | the name shown everywhere: page, logo, app, notifications     |
| `POSTERM_LANG`     | `en`             | `en` or `fr`: UI, push, CLI, logs, API errors                 |
| `POSTERM_USER`     | —                | started as root, posterm becomes this user (and so do labs)     |
| `POSTERM_GROUP`    | the user's group | matching primary group                                        |
| `POSTERM_ADDR`     | `127.0.0.1:8206` | hub listen address                                            |
| `POSTERM_TOKEN`    | generated        | the one token (hub: web access; spoke: access to the hub)     |
| `POSTERM_HUB`      | —                | hub URL, for `posterm join`                                     |
| `POSTERM_NAME`     | hostname         | the machine's name on the wall                                |
| `POSTERM_DATA`     | `~/.posterm`       | registry, journal, tmux socket, push keys                     |
| `POSTERM_QUIET`    | `3`              | seconds of silence before a lab stops being "running"         |
| `POSTERM_QUESTION` | `src/engine.go`  | regex of dialogs that count as a question (replaces default)  |

Every word the program says comes from `src/web/i18n/<lang>.json`, the same
files for Go, the page and the browser. Adding a language is adding a file.

## Deploying the hub

`config/` holds three reference files: `create-usergroup.sh` (the labs' user),
`posterm.service` (systemd) and `Caddyfile.example` (a locked-down reverse proxy:
IP allowlist, TLS 1.3, strict headers, optional client certs). WebSocket and
SSE go through Caddy untouched. If your proxy caps request bodies, lift the cap
for the upload route only: `docs/caddy.md`.

The examples assume posterm lives in `/opt/posterm` (binary, `.env`, `data/`) and
projects under `/srv`. Adjust the paths, `posterm.service` included.

As a systemd service, **`KillMode=process`** is not optional. Without it,
restarting the hub kills the tmux server, and every lab with it.

### Which user the labs run as

Labs run as the hub, and the hub runs as whatever `.env` says (`POSTERM_USER` /
`POSTERM_GROUP`). Started as root (systemd, sudo), posterm drops to that identity
before touching anything and hands its data dir over. Labs inherit the user's
groups, `HOME` and shell (`/bin/bash` if the account has none, like
`www-data`). Leave both empty and posterm stays whoever started it.

- **root**: agents can do anything (`systemctl`, `/etc`, Caddy…). But codex's
  sandbox is a *user namespace*, and in there root loses its override on files
  owned by other users: if the projects belong to `www-data`, codex can only
  write to `/tmp`. Fix: `codex -s danger-full-access`.
- **a dedicated user** (`posterm`): plain Unix permissions, inside the sandbox
  too. Nothing that needs root, though: keep that for yourself, or grant it
  command by command with sudo (below).
- **www-data**: the projects already belong to it. But that's also your web
  server's user, and now it runs agents. Your call.

A tmux server belongs to one user. Changing identity means stopping the running
labs: the hub refuses to start while another user's tmux server holds the
socket, and tells you the command to run.

Switching to `posterm:posterm`:

```sh
# 1. user and group (locked system account, real shell, private HOME,
#    extra group www-data, umask 002). Idempotent.
config/create-usergroup.sh

# 2. projects: owned by posterm, www-data keeps access through the group
for p in /srv/project-a /srv/project-b; do
  chown -R posterm:www-data "$p"
  chmod -R g+w "$p"
  find "$p" -type d -exec chmod g+s {} +   # new files stay in group www-data
done

# 3. stop the old hub AND its tmux server (this kills the running labs)
tmux -S /opt/posterm/data/tmux.sock kill-server

# 4. in .env: POSTERM_USER=posterm and POSTERM_GROUP=posterm. .env stays root-only:
#    the hub reads it before dropping privileges, and the labs have no business
#    reading the token.
chown root:root /opt/posterm/.env && chmod 600 /opt/posterm/.env
#    then the service
cp /opt/posterm/config/posterm.service /etc/systemd/system/
systemctl daemon-reload && systemctl enable --now posterm
journalctl -u posterm -f
```

Then open a `bash` lab on the wall (it now runs as `posterm`) and install and log
in the agents there (`claude`, `codex login`…). Root's binaries and logins
(`/root/.local`, `/root/.nvm`, `~/.codex`, `~/.claude`) are out of posterm's
reach; copy the settings you want, e.g. `/root/.codex/config.toml`. Then
respawn the labs: `labs.json` is kept, the agents' conversations are not (they
stay under `/root`).

`posterm lab …` run as root from `/opt/posterm` drops to the `.env` identity too.
To watch a lab from the machine itself:
`sudo -u posterm tmux -S /opt/posterm/data/tmux.sock attach -t <id>`.
While you're attached, that lab sends no push: you're watching it.

To give a dedicated user a few root powers back, a `/etc/sudoers.d/posterm`
(edit with `visudo -f`) that allows exactly the commands you mean:

```
posterm ALL=(root) NOPASSWD: /usr/bin/systemctl restart project-a.service, /usr/bin/systemctl reload caddy
```

## Security

The hub hands out a shell on every connected machine. That is the job. It is
built for one operator, and it doesn't pretend otherwise.

- One token, compared in constant time. `HttpOnly` cookie for browsers,
  `Bearer` for spokes. No token, no data: you get an empty page and a login box.
- Any request with a foreign `Origin` is refused (CSRF, WebSocket hijacking).
- The hub listens on `127.0.0.1` by default. TLS is the reverse proxy's job, and
  so is the IP allowlist.
- A spoke listens on nothing.
- Uploads are confined to their target dir: the server only accepts the
  bash's cwd, or a dir it handed out for that lab in the last 15 minutes.
  Every path, and every entry of every zip, is checked before a single byte is
  written.
- Labs run as the hub's user (`POSTERM_USER`). Make it a dedicated one.

## Roadmap

| phase                       | status                                                                                    |
| --------------------------- | ----------------------------------------------------------------------------------------- |
| 0 — single binary, auth     | done: one binary, one port; token + cookie instead of an IP allowlist                     |
| 1 — durable jobs, journal   | done: job = tmux session; append-only `journal.jsonl` (transitions, not every byte)       |
| 2 — human in the loop       | done: the `question` state, `posterm state`, answer from the wall, broadcast or push        |
| 3 — Web Push, badge, inbox  | done: VAPID, PWA badge = number of questions, log "since you last looked"                 |
| 7 — dashboard, PWA          | done: the wall, installable                                                               |
| 4 — unified chat            | no. You talk to agents in their terminal. That's what terminals are for.                  |
| 5 — wiki as memory          | no                                                                                        |
| 6 — posterm.do as a tool      | no                                                                                        |

## Known limits

- After a **machine reboot**, labs are still on the wall but dead. One click on
  "respawn" and the command starts from scratch.
- Budgets (turns, tokens) are the agent's business, not ours.
- A lab without a command opens a login shell, which lands wherever your shell
  profile sends it. Give it a command and the lab's dir is guaranteed.
- Two languages so far: English and French.
- The service worker (notification display, Reply button) hasn't been tested on
  a real phone yet; server-side sending is verified.
- Directory uploads are zipped without zip64: beyond 4 GB, `Alt` + drop.
- `journal.jsonl` is rotated at startup past 4 MB into `journal.1.jsonl`,
  one generation kept. Older history is gone.
- The Mac package is tested on Linux with macOS stubbed out. Not yet on real
  hardware: Gatekeeper, launchd and push in the Dock app still need a Mac.

## Layout

```
README.fr.md     this file, in French (keep both in sync)
go.mod, go.sum   the Go module (the only Go files at the root)
.env.example     .env template (the real .env is per installation, not versioned)
src/main.go      the four commands, .env loading
src/i18n.go      POSTERM_LANG, T(): the dictionaries, for Go and the browser
src/user.go      the labs' identity (POSTERM_USER / POSTERM_GROUP)
src/engine.go    one machine: lab registry, tmux, state detection, API, terminals, uploads
src/engine_test.go  go test ./src: path confinement, zips, upload targets, config
src/hub.go       the hub: token, spoke tunnels, journal, SSE stream, push
src/web/         the wall: vanilla HTML/CSS/JS, vendored xterm.js, embedded in the binary
src/web/i18n/    en.json, fr.json: every user-facing string
bin/build.sh     build (timestamped backups in bin/backup/)
bin/release.sh   release assets: 4 static binaries, the Mac zip, SHA256SUMS
macos/           the Mac package: build.sh -> zip, install/uninstall.command, guide
config/          create-usergroup.sh, posterm.service, Caddyfile.example
docs/design.md   why it's built this way: every choice, what we refused, what it costs
docs/caddy.md    lifting Caddy's body limit for uploads
docs/img/        screenshots (demo labs, nothing real)
docs/x/          social cards: HTML sources, PNGs, render.cjs
site/            posterm.sh: static pages (en, fr), the design doc page, the install script
LICENSE          MIT
data/            POSTERM_DATA if .env puts it there (registry, journal, tmux socket), not versioned
```

## License

MIT — © 2026 L3DLP. See [LICENSE](LICENSE).
