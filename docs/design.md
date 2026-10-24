# Why POSTERM is built this way

POSTERM rests on one idea: **an agent job is a terminal that runs on its own.**
Everything below follows from taking that sentence literally. If a job is a
terminal, the best job runner is the thing that already keeps terminals alive
without you (tmux), the best job view is the terminal itself (xterm.js), and
the job's state is whatever its screen says.

This document goes through every choice that matters. For each one it gives
what we picked, why, what we turned down, and what it costs. Nothing here is
aspirational: every claim maps to code you can read in an afternoon, under
3,000 lines of Go and JS plus a vendored xterm.js.

- [The shape of the thing](#the-shape-of-the-thing)
- [tmux is the job queue](#tmux-is-the-job-queue)
- [A lab is a dir plus a command](#a-lab-is-a-dir-plus-a-command)
- [One static binary, four faces](#one-static-binary-four-faces)
- [Every machine speaks the same API](#every-machine-speaks-the-same-api)
- [Spokes dial out](#spokes-dial-out)
- [State is read off the screen](#state-is-read-off-the-screen)
- [The filesystem is the IPC](#the-filesystem-is-the-ipc)
- [One token, one operator](#one-token-one-operator)
- [Threat model](#threat-model)
- [No build step in the browser](#no-build-step-in-the-browser)
- [The journal records transitions, not bytes](#the-journal-records-transitions-not-bytes)
- [Push, with a Reply field](#push-with-a-reply-field)
- [A bash under every lab](#a-bash-under-every-lab)
- [Uploads land whole or not at all](#uploads-land-whole-or-not-at-all)
- [Selection goes through OSC 52](#selection-goes-through-osc-52)
- [One dictionary per language, everywhere](#one-dictionary-per-language-everywhere)
- [Dropping privileges before tmux](#dropping-privileges-before-tmux)
- [The Mac package touches no code](#the-mac-package-touches-no-code)
- [Releases are signed](#releases-are-signed)
- [One line installs, and finishes the job](#one-line-installs-and-finishes-the-job)
- [What we will not build](#what-we-will-not-build)
- [Rules we keep](#rules-we-keep)
- [Read the code](#read-the-code)

## The shape of the thing

```
 browser / phone (PWA)
   │  HTTPS (reverse proxy: TLS, IP allowlist)
   ▼
 ┌──────────── hub: posterm ────────────┐
 │  auth guard ─ static wall ─ SSE      │
 │  /m/<machine>/…  ── route ──┐        │
 │                             │        │
 │  local Engine ◄─ in-process─┤        │
 │                             │        │
 │  yamux client ◄─────────────┘        │
 └──────▲───────────────────────▲───────┘
        │ WebSocket (dialed out │ by the spoke)
 ┌──────┴────────┐       ┌──────┴────────┐
 │ spoke: posterm│       │ spoke: posterm│
 │ join  Engine  │       │ join  Engine  │
 │      tmux     │       │      tmux     │
 └───────────────┘       └───────────────┘
```

One binary plays every role. The hub serves the wall and runs its own
machine's labs. A spoke runs labs and publishes them to the hub. On every
machine an `Engine` owns a private tmux server. The hub never holds lab state
of its own: it reads it from the engines once a second.

## tmux is the job queue

**The choice.** Each lab is a tmux session on a private tmux server whose
socket lives in the data dir (`$POSTERM_DATA/tmux.sock`, started with our own
`tmux.conf`). That server *is* the scheduler, the process supervisor, the
scrollback store and the persistence layer. POSTERM only watches it and talks
to it.

**Why.** tmux has solved "keep this process and its screen alive while nobody
watches" for decades. It survives SSH drops, browser crashes and our own
restarts. It already knows everything we need: whether the pane is dead and
with which exit code (`remain-on-exit on`), when the screen last changed
(`window_activity`), where the cursor is, how many clients are attached, and
what the screen shows (`capture-pane`). A single `tmux list-panes -a` call per
second returns all of it for every lab at once.

The generated `tmux.conf` is short and every line has a reason:

| line                     | why                                                           |
| ------------------------ | ------------------------------------------------------------- |
| `status off`             | the wall is the status bar                                    |
| `remain-on-exit on`      | a dead lab keeps its last screen and exit code: grey post-it  |
| `history-limit 20000`    | scrollback you can actually use                               |
| `mouse on`               | wheel scrolling and selection inside the browser terminal     |
| `escape-time 0`          | Esc reaches TUIs (vim, agent dialogs) without a delay         |
| `focus-events on`        | TUIs that care about focus get it                             |
| 256 colors + `Tc`        | agents print in color; truecolor where the client has it      |

**What we refused.**

- A database of jobs. It would duplicate what tmux knows, and the two would
  disagree the first time the hub crashed.
- Our own process supervisor (fork, PTY bookkeeping, log files). That's
  rewriting tmux badly, and our jobs would die with our process.
- A container per job. Agents need your real toolchain, your real login and
  your real files. A container makes all three someone else's problem, then
  yours again.

**The cost.** Labs survive the hub, so the hub must not kill them on the way
out. Under systemd that means `KillMode=process` (the sample unit sets it):
without it, a restart takes the tmux server, and every lab, down with it.
The macOS LaunchAgent sets `AbandonProcessGroup` for the same reason. And
tmux does not survive a machine reboot: after one, labs are still in the
registry but grey, and "respawn" starts the command from scratch.

## A lab is a dir plus a command

**The choice.** A lab is four fields in `labs.json`: id, name, dir, command.
It runs as `$SHELL -lic 'cd <dir> && <cmd>'`, a login *and* interactive shell,
with `POSTERM_LAB` and `POSTERM_DATA` set. With no command, it is a plain
login shell. Respawning reuses the session (`respawn-pane -k`), so the post-it
and anything attached to it stay put.

**Why.** Agents are installed per user, through nvm, `~/.local/bin`, shell
functions and profile tweaks. The only PATH that reliably works is the one
your terminal has, and that one comes from a login + interactive shell. The
explicit `cd` comes after the profile has run, because profiles love to `cd`
somewhere.

The registry is a file re-read on every poll and written atomically
(temp + rename). That makes it an API: `posterm lab <dir> [cmd…]` from a
shell and `+ lab` on the wall both write the same file, and the engine picks
the change up within a second. Ids are slugs of the name, deduplicated with
`-2`, `-3`…

**What we refused.** Job definitions with retries, schedules, environment
blocks and resource limits. An agent job is interactive by nature: it asks
questions, you answer. A YAML schema for that is a costume.

**The cost.** A command that only works in a non-login shell, or a profile
that prints a wall of text, shows up as is. A lab without a command lands
wherever your profile sends it, since there is nothing to `cd` before.

## One static binary, four faces

**The choice.** One Go binary, built with `CGO_ENABLED=0`, with the whole web
UI embedded (`//go:embed web`). It has four entry points:

```
posterm                       the hub: web wall + this machine's labs
posterm join [hub-url]        a spoke: publish this machine's labs to a hub
posterm lab <dir> [cmd…]      create and start a lab from the shell
posterm state <question|idle> [text…]   from inside a lab: say where it is
```

Configuration is environment variables or a `.env` in the working directory,
and the real environment always wins.

**Why.** Deploying is copying one file. A spoke is the same file plus a URL
and a token. Hub and spokes can't drift apart in version of the UI because
there is only one UI, served by the hub. Go gives us a static binary, a
proper HTTP stack and goroutines for the per-terminal pumps, and it
cross-compiles to macOS from Linux without a Mac in the loop.

**What we refused.**

- Separate server, agent and CLI packages: three things to version and keep
  in step for no gain.
- A Node or Python runtime on every machine.
- A config file format with its own parser. `KEY=VALUE` lines are enough, and
  every installation-specific value lives there and nowhere else.

**The cost.** Any change to the UI is a rebuild. That's fine: a build is a
few seconds.

## Every machine speaks the same API

**The choice.** `Engine.Handler()` is the entire HTTP surface of one machine:
list labs, create, start, stop, delete, type keys, complete directory names,
the terminal WebSocket, the companion bash and uploads. The hub mounts every
engine under `/m/<machine>/…`. For its own machine the request goes
in-process to the handler. For a spoke it goes through
`httputil.ReverseProxy`, into the tunnel.

**Why.** The hub stays dumb, which is the point. There is no hub-side copy of
the lab logic, no RPC layer, no message types. A request for a spoke's
terminal is an ordinary HTTP request with an Upgrade header, proxied as is.
Adding a feature to labs means adding a route to the engine, and it works on
every machine at once.

`/m/` is mounted on a raw `http.ServeMux`, outside gin. That's not
style: each engine is its own gin router and must own the raw
`ResponseWriter`, otherwise its WebSocket upgrade never reaches the wire.

**What we refused.** A central server that owns labs and pushes commands to
dumb agents. Labs belong to the machine they run on. The hub can vanish and
every lab keeps going, and `tmux attach` on the machine still works.

**The cost.** The wall needs the hub to aggregate state, so the hub polls
every spoke's `/api/labs` once a second through the tunnel. Cheap, but it is
polling.

## Spokes dial out

**The choice.** `posterm join https://hub.example.org` opens one outbound
WebSocket to `/join`, authenticated with `Authorization: Bearer <join token>`.
Both ends wrap that WebSocket in a yamux session. The **hub is the yamux
client**: every stream it opens is a fresh connection to the spoke, on which
the spoke runs `http.Serve` with its engine handler.

```
hub                                       spoke
 http.Transport{Dial: sess.Open}  ──►  yamux server ──► http.Serve(engine)
        ▲                                          (no listening port)
        └── ReverseProxy for /m/<spoke>/…
```

**Why.** The spoke listens on nothing. No port to open, no firewall rule, NAT
and home routers don't matter. And since the hub opens the streams, the rest
is plain HTTP: the same handler, the same proxy, the same WebSocket upgrade
for terminals as on the hub's own machine.

Reconnection is the spoke's job: exponential backoff from 1 s to 30 s,
forever. A 401 (wrong token) or 409 (name taken, or equal to the hub's name)
is fatal instead, because retrying won't fix it. If a spoke reconnects under
the same name, the hub closes the old session and keeps the last known labs
until fresh ones arrive.

**What we refused.**

- SSH tunnels managed by hand.
- A VPN as a requirement. You may use one; POSTERM doesn't need it.
- gRPC or a custom framing. HTTP over yamux reuses everything we already have.

**The cost.** A spoke's labs show as offline the moment the tunnel drops,
until it reconnects. The tunnel carries every terminal byte for that spoke,
so a slow link is a slow terminal.

## State is read off the screen

**The choice.** Once a second, for every lab, the engine decides one of four
states:

| order | condition                                                        | state      | color  |
| ----- | ---------------------------------------------------------------- | ---------- | ------ |
| 1     | the pane is dead                                                 | `dead`     | grey   |
| 2     | the screen changed less than `POSTERM_QUIET` s ago (default 3)   | `running`  | red    |
| 3     | the cursor line looks like a prompt, or the screen shows a dialog| `question` | yellow |
| 4     | anything else                                                    | `idle`     | green  |

The prompt test runs on the line under the cursor, with box-drawing frames
stripped so `?$` can see the text: `[y/N]`, `(y/n)`, `yes/no`, `password`,
`passphrase`, `press enter`, a trailing `?`, plus their French forms. The
dialog test runs on the last 12 non-empty lines of the screen, with a regex
for agent TUIs ("permission required", "do you want to proceed", a `❯ 1. Yes`
menu…) that `POSTERM_QUESTION` replaces entirely.

The screen is only captured when `window_activity` moved, so a quiet wall
costs one `tmux list-panes` per second, total.

**Why.** It works with every program, unmodified: `claude`, `codex`, `crush`,
a script, `apt upgrade`. Nobody has to adopt a protocol for the wall to be
useful on day one. "The screen stopped moving and the cursor sits after a
question mark" is exactly how a human tells that a terminal waits for them.

**What we refused.** Parsing each agent's output format, or wrapping agents
to intercept their I/O. Formats change every release, and wrappers break TUIs.

**The cost.** It's a heuristic and it says so. A long silent compile looks
idle. A TUI with a new dialog wording looks idle until you add it to the
regex. Hence the next section.

## The filesystem is the IPC

**The choice.** `posterm state question "Delete build/?"` or
`posterm state idle`, run from inside a lab, writes one file:
`$POSTERM_DATA/etat/<lab>` holding the state and the note. The engine reads
it on the next poll. It overrides the guess, except for a dead pane, until
the screen moves again (activity more than 2 s newer than the file), then the
file is deleted and guessing resumes.

**Why.** Agents have hooks ("on stop", "on notification"). A hook can run a
command. That's all the integration needed: no network, no token, no socket,
no daemon. The engine already injects `POSTERM_LAB` and `POSTERM_DATA` into
every lab, so the command knows where to write. It runs before any identity
switch and touches nothing but that one file.

**What we refused.** A local HTTP endpoint for hooks. It would need auth, a
port, and a client in every hook, all to move a few bytes into a file.

**The cost.** It only works inside a lab started by POSTERM, and the declared
state is only as accurate as the hook that sets it.

## One token, one operator

**The choice.** POSTERM is built for one operator, so there is exactly one
operator secret. Spokes get a narrower one of their own.

- The token comes from `POSTERM_TOKEN`, or is generated once (24 random
  bytes, hex) into `$POSTERM_DATA/secrets/token` with mode 600.
- Each spoke gets its own token: `posterm spoke add <name>` on the hub prints
  it once, `posterm spoke revoke <name>` cuts that machine's tunnel within a
  second. The hub keeps only SHA-256 hashes, in `$POSTERM_DATA/spokes.json`
  (owned by whoever ran the command, root on a hub started as root, so a lab
  can't add itself a token). A spoke token opens `/join` for its own name and
  nothing else: no API, no wall, no other machine's name.
- The hub-wide join token (`POSTERM_JOIN_TOKEN`, or generated into
  `secrets/join-token`) and the operator token still open `/join` for names
  that have no token of their own, with a warning in the log. A name that
  has its own token can't be joined with them anymore, and a spoke still
  connected that way is cut when its name gets one.
- Browsers log in once and get an `HttpOnly`, `SameSite=Lax` cookie valid for
  a year, `Secure` when the request came over TLS. Its value is a SHA-256
  derived from the token, never the token itself. Rotate the token and every
  browser is logged out.
- Spokes send `Authorization: Bearer <join token>`, and only over TLS: a
  spoke refuses a cleartext hub URL unless it is this very machine
  (`POSTERM_INSECURE=1` overrides).
- Both comparisons are constant-time. A failed login waits a second, and
  failures are budgeted: five per client, then one every 12 s, and thirty for
  the whole hub, then one every 2 s. Past that it's a 429 with `Retry-After`.
  The client is the first `X-Forwarded-For` hop when the request comes from
  the reverse proxy on this machine, its socket's address otherwise.
- Any request that carries an `Origin` header must come from the hub's own
  host, or it's a 403. That kills CSRF and cross-site WebSocket hijacking in
  one check.
- Only the static shell and `/api/login` are public. `/api/*` and `/m/*`
  need the operator's auth, `/join` the join token.
- None of these secrets ever reaches a lab: see
  [Dropping privileges before tmux](#dropping-privileges-before-tmux).
- The hub listens on `127.0.0.1:8206` by default.

**Why.** The hub hands out a shell on every connected machine. User accounts,
roles and an OAuth dance would add attack surface without adding safety for
the single person who should be using it. TLS, the IP allowlist and optional
client certificates are a reverse proxy's job, done better by Caddy than by
us. `config/Caddyfile.example` stacks all three and ships a strict CSP:
`script-src 'self'`, no inline script anywhere, `unsafe-inline` for styles
only because xterm.js injects its own.

**What we refused.** Multi-user. Sharing your agents' shells with a team is a
different product with a different threat model.

**The cost.** Whoever has the operator token has a shell on every machine.
Keep the hub behind the proxy, and run the labs as a dedicated user (below).
A spoke that still uses the hub-wide join token can take the place of any
name that has no token of its own: give every spoke its own.

## Threat model

Who can do what, and what POSTERM does about it.

- **The operator** holds the operator token: the wall, the API, a shell in
  every lab on every machine. That's the product. Protected by the token, the
  reverse proxy (TLS, IP allowlist, optional client certificates) and the
  login budget.
- **The labs** are agents. They read web pages, issues and READMEs that
  strangers wrote, so assume one of them is hostile. A lab has a shell in its
  own project; it must not get anything more. It can't read the hub's secrets
  or environment, can't replace them, and can't add itself a spoke token
  ([Dropping privileges before tmux](#dropping-privileges-before-tmux)). This
  holds when the hub starts as root with a `POSTERM_USER`; started as the labs'
  own user, the hub has nothing to hide its files behind.
- **A spoke** is another machine, maybe less trusted than the hub. Its token
  opens `/join` for its own name: it serves its labs to the hub and receives
  what the operator types into them, nothing else. Revoking it cuts it off.
- **The network** carries the token. TLS is the proxy's job on the hub side,
  and `posterm join` refuses a cleartext remote hub.
- **The release channel**: the installer only accepts binaries whose
  `SHA256SUMS` carries the project's signature
  ([Releases are signed](#releases-are-signed)).
- **Afterwards**: every action the operator takes through the API (create,
  start, stop, delete, keys, uploads) is journaled, without the typed text.

Out of scope: someone with the operator token or the operator's browser,
root on the hub, and an agent damaging the project it was given. It has a
shell there on purpose; that's what git is for.

## No build step in the browser

**The choice.** The wall is `index.html`, `style.css`, `app.js` (about a
thousand lines of vanilla JS) and a vendored xterm.js with its fit addon. No
npm, no bundler, no framework. One `EventSource` on `/api/stream` paints the
wall. Each open terminal is one WebSocket to the engine.

The SSE side is deliberately boring. The hub diffs the world once a second
and sends a frame only when it changed, plus a comment every 20 s to keep
proxies from closing the stream. Each browser has a one-slot channel: a slow
client drops the stale frame and gets the latest one. Nothing queues.

A terminal is the shortest path we could find:

```
xterm.js ──WebSocket──► hub guard ──► route ──► Engine.terminal
                                                   │
                                     pty ◄── tmux attach-session -t =<lab>
```

Binary frames carry raw bytes both ways. Text frames carry
`{"cols":…,"rows":…}` resizes. Each browser terminal is its own
`tmux attach` in its own PTY, so tmux handles several viewers on the same lab
exactly like several real terminals.

**Why.** No build means no toolchain to rot, no lockfile to audit, and the
code you read is the code that runs. One SSE stream is enough for a wall that
changes at most once a second. WebSockets are reserved for the one thing that
needs bytes in both directions.

**What we refused.** React and friends, a websocket-for-everything protocol,
a terminal emulator of our own.

**The cost.** We write DOM code by hand, and per-browser preferences (lab
order, bash on/off, bash height) live in `localStorage` only.

## The journal records transitions, not bytes

**The choice.** `journal.jsonl` gets one JSON line per event worth a human's
attention:

- a lab turns `question`;
- a lab turns `idle` after at least 10 s of `running` (real work, not a
  blip);
- a lab dies with an exit code.

Each line holds the time, machine, lab, from/to states and a text: the note
from `posterm state`, else the last lines of the screen. The file is
append-only; the hub keeps the last 300 events in memory for the log panel.

It also records what the operator did through the API, once it succeeded:
a lab created (dir and command), started, stopped, deleted, keys sent
(`12 chars + Enter`, never the text itself), files uploaded (dir and size),
and whether it came from the wall or the API. These lines never count as
news, and the log panel can hide them. Terminal keystrokes go over the
WebSocket and are not journaled: that would be a keylogger.

**Why.** "What happened while I was away" is a list of transitions. Recording
terminal output would be a second, worse scrollback. tmux already keeps the
real one.

**What we refused.** Logging every byte, metrics, a time-series store.

**The cost.** History is bounded, crudely: past 4 MB (a few hundred bytes
per event, so roughly a year of a busy wall), the hub renames it to
`journal.1.jsonl` at startup, keeping one generation. Anything older is gone.

## Push, with a Reply field

**The choice.** The same events trigger Web Push (VAPID keys generated once
into `push.json`). A `question` notification carries a **Reply** action:
type a line in the notification, and the service worker posts it to the
lab's `/keys` endpoint, Enter included. The app badge counts the yellow
post-its. No push is sent for a lab someone is attached to: tmux's attached
client count is the definition of "watching", and a real `tmux attach`
counts too.

**Why.** The point of the wall is to stop watching terminals. Push finishes
the job: the yellow post-it reaches your pocket, and for a yes/no question
you answer without opening anything. Typing goes through `tmux send-keys -l`
(literal), then a 150 ms pause before Enter so TUIs can tell typing from a
paste.

**What we refused.** Email, chat bots, a third-party notification service.
Web Push is standard, free, and its payload goes from the hub to the
browser's push service without anyone else in the loop.

**The cost.** Push needs HTTPS and a browser that supports it. Inline reply
depends on the platform's notification UI.

## A bash under every lab

**The choice.** Each lab can have a companion session `<id>__sh`: a login
shell in the lab's dir, shown under the agent's terminal. It's created on
demand when the browser opens it, killed with the lab, and it is **not** a
lab: not in the registry, not watched, no post-it, no push. `exit` ends it
for good (no `remain-on-exit`); the next open starts a fresh one. Ids are
slugs, so `__` never collides with another lab.

**Why.** You want `git diff` while the agent works, in the same dir, without
opening another tab or typing into the agent's prompt.

**What we refused.** Making it a second lab. Its state would pollute the wall
and its silence would look like a question.

**The cost.** One more tmux session per lab where you open it.

## Uploads land whole or not at all

**The choice.** Drop files or folders on a lab's bash and they land in that
bash's **current** directory (`#{pane_current_path}`), wherever you `cd`'d.

1. `POST …/sh/check` returns that dir and which of the dropped paths already
   exist there. The browser asks before overwriting anything.
2. The browser zips folders itself: native `CompressionStream('deflate-raw')`,
   a hand-rolled CRC-32, hand-written zip headers. Media and archives are
   stored, not deflated again.
3. `POST …/sh/upload?dir=…` streams a multipart form: a `manifest` part
   (`{"files":[…],"dirs":[…],"unzip":[…]}`), then one `file` part per path,
   in order. Plain files go straight to disk. Zips are spooled to a hidden
   temp file next to their target (`archive/zip` needs to seek), extracted
   into `<name>/`, and not kept.
4. Every path, and every entry of every zip, goes through `inside()` before a
   byte is written: no `..`, no absolute path, no backslash, no NUL.
   Symlinks in zips are skipped, and so are `__MACOSX/` and `.DS_Store`. A
   zip whose entries all sit in a folder named like the zip isn't nested
   twice. Executable bits survive.
5. The server doesn't trust `dir`: it must be the bash's current dir, or a
   dir `check` handed out for this lab in the last 15 minutes (you may `cd`
   while the browser zips). Anything else gets a 403.
6. Files are opened with `O_EXCL` unless the user said yes to overwriting. An
   `undo` log records every file and top directory the upload created. Fail
   or cancel at any point and it's all removed.

`Alt` + drop sends files raw, no zip, no unpacking. The upload goes through
XHR because `fetch` still can't report upload progress.

**Why.** Getting a spec, a dataset or a screenshot to the agent is the most
common thing you do after typing. Making it a drop on the right terminal
removes scp from your day. All-or-nothing matters because a half-unpacked
folder in a repo is worse than an error.

**What we refused.** A file manager. One drop target with a visible
destination is the whole feature.

**The cost.** No zip64: a zipped folder must stay under 4 GB and 65,535
entries (use `Alt` + drop beyond that). A file overwritten after your "yes"
can't be restored by a rollback. Reverse proxies often cap request bodies:
lift the cap for the upload route only (`docs/caddy.md`).

## Selection goes through OSC 52

**The choice.** tmux runs with `mouse on`, so a drag in the browser terminal
selects *in tmux*. tmux then sends the selected text to its client as an
OSC 52 escape sequence (`set-clipboard external` is tmux's default).
`app.js` registers an xterm.js handler for OSC 52, decodes it, and pops a
two-item menu where the mouse was released: **copy**, or **save as .md**
(named `<lab>-<date>-<time>.md`). It only pops if a mouse button came up in
that terminal within the last 3 s, so a program writing OSC 52 on its own
doesn't trigger it. Shift+drag selects in xterm.js instead and opens the same
menu. A click elsewhere or Esc closes it without copying.

**Why.** Before this, a drag selected text that never left tmux: it was
copied nowhere. Turning mouse mode off would lose wheel scrolling through
tmux history. Catching the sequence tmux already emits fixes it with zero
change on the server side.

**What we refused.** Turning tmux mouse mode off; copy-on-select (it
clobbers your clipboard every time you click-drag to read).

**The cost.** Clipboard writes need a secure context. Without HTTPS the
browser may refuse, and there's an `execCommand` fallback that sometimes
works.

## One dictionary per language, everywhere

**The choice.** Every user-facing string (UI, push, CLI usage, logs, API
errors) lives in `src/web/i18n/<lang>.json`, picked by `POSTERM_LANG` (`en`
by default, `fr` available). A missing key falls back to English, and startup
logs which keys each language lacks.

- Go uses `T(key, "name", value…)`.
- The page is filled once at startup: `{{t:key}}`, `{{LANG}}` and the title
  are replaced in `index.html` and the manifest, HTML- or JSON-escaped as the
  file needs. No flash of untranslated text.
- The browser gets the rest as `/i18n.js` (`window.I18N = {…}`), a real file
  so the CSP never needs an inline script. Plurals go through
  `Intl.PluralRules`.
- The service worker has no window and no dictionary, so the hub puts the
  words it needs (the Reply label and placeholder) in each push payload.

**Why.** One source of truth for three runtimes. Adding a language is adding
a file.

**What we refused.** gettext, an i18n library, or per-runtime string tables
that drift apart.

**The cost.** The language is per installation, not per browser.

## Dropping privileges before tmux

**The choice.** Labs run as the hub's user. Started as root (systemd, sudo),
`becomeUser()` switches the whole process to `POSTERM_USER` /
`POSTERM_GROUP` before anything touches tmux or the data dir: it resets
`HOME`, `USER`, `LOGNAME` and `SHELL`, clears the XDG variables, reads the
hub's secrets (still as root), hands the data dir's files over, then calls
`setgroups`, `setgid` and `setuid`, in that order. A service account whose shell is `nologin` gets `/bin/bash`,
because labs need a real shell. With both variables empty, posterm stays
whoever started it.

A tmux server belongs to one user. If a server owned by someone else still
holds the socket, the hub refuses to start and prints the command to fix it.
The owner is read from the tmux server's process, not the socket file, so a
`chown` of the data dir can't fool it. A stale socket with nobody behind it
is simply removed.

**Keeping the secrets out of the labs.** The labs are agents. Agents read
web pages, issues and READMEs written by strangers, and a prompt injection is
one paragraph away. So a lab must not be able to read anything that would let
it drive the hub, and with it every other lab:

- **The environment.** The tmux server, hence every lab, gets the hub's
  environment minus whatever came from `.env` and every `POSTERM_*` setting.
  A lab gets `POSTERM_LAB` and `POSTERM_DATA` and nothing else of ours. At
  startup the hub also strips those keys from the global environment of a tmux
  server started by an older version.
- **The files.** The operator token, the join token and the VAPID key pair
  live in `$POSTERM_DATA/secrets/`, owned by root, mode 700, files 600. The
  hub reads them before the switch and keeps them in memory. `push.json` only
  keeps the subscriptions.
- **The directory.** The data dir itself stays root's, group-writable by the
  labs' group and sticky (`01770`). The hub, now that user, can create and
  replace its own files in it. Nobody but root can rename or remove
  `secrets/`: in a directory the labs owned, a `mv` of it would have needed
  nothing more than write access to the data dir.
- **Planted secrets.** A `secrets/` that isn't a root-owned directory (a lab
  made it, or it's a symlink) is moved aside and never read. Secrets are
  regenerated instead of trusted.

Started as the labs' own user (no `POSTERM_USER`), the hub and the labs are
the same account and nothing on disk can be hidden from them. That's the
setup for a laptop, not for a hub that agents share.

**Why.** systemd starts services as root, and the `.env` holding the token
can stay root-only: it's read before the switch, and the labs never see it.
The labs, meanwhile, run as an account that can write to the projects and
nothing else (`config/create-usergroup.sh` creates one, idempotently).

**What we refused.** Running agents as root by default. It's allowed, it's
documented, and it's your call. There's also a real trap in it: codex's
sandbox is a user namespace, and in there root loses its override on files
owned by other users, so it can only write to `/tmp`.

**The cost.** Changing identity means stopping the running labs, since their
tmux server belongs to the old user. Labs that were already running when you
upgraded from 0.1.0 still carry whatever environment they started with:
rotate the token (a new `POSTERM_TOKEN`, or delete `secrets/token`; then
restart) and restart them. Agents must be installed and logged in
as the labs' user.

## Releases are signed

**The choice.** `bin/release.sh` signs `SHA256SUMS` with an SSH key
(`ssh-keygen -Y sign`, namespace `posterm-release`). The public half is
`site/allowed_signers`, embedded in `site/install`, which verifies the
signature before it checks a single sum. No signature, a bad one, or an
OpenSSH without `-Y` (older than 8.1): nothing gets installed.
`POSTERM_UNSIGNED=1` skips the check, loudly. In CI the key is the
`RELEASE_SIGNING_KEY` secret; `release.sh` refuses to build an unsigned
release unless told so, and checks that the installer embeds the right key.

**Why.** Checksums fetched from the same release prove the download is
intact, not who published it: whoever can upload a release can upload
matching sums. A signature moves the trust from "can push to GitHub" to
"holds the key".

**What we refused.** minisign or cosign: one more tool to install before
the installer can run. ssh-keygen is already on every Linux and Mac.

**The cost.** The key must be kept, and rotating it means shipping an
installer with the new one. Releases before 0.2.0 are unsigned, and the
current installer refuses them.

## The Mac package touches no code

**The choice.** `macos/build.sh` cross-compiles both darwin binaries from
Linux and zips them with a double-click installer. The installer:

- checks macOS 12 or later and picks the Apple silicon or Intel binary;
- installs tmux with Homebrew if needed (and Homebrew, if you agree);
- puts `~/.local/bin/posterm` in place, unquarantined and ad-hoc signed;
- writes `~/.posterm/.env` once, in French if the Mac is;
- registers a LaunchAgent: started at login, restarted on crash,
  `AbandonProcessGroup` so updates never kill the labs, `SHELL` set to your
  login shell and a PATH that includes Homebrew;
- opens Safari with the token in the clipboard.

The "app" is Safari's **File → Add to Dock**: its own window, icon and
notifications.

**Why.** The binary already runs on macOS unchanged. Everything Mac-specific
is packaging, so it stays in `macos/`, in bash 3.2 (the one macOS ships).

**What we refused.** Tauri or Wails. A native webview means Rust or cgo, a
Mac to build on, and no Web Push inside an embedded webview, so notifications
would have to be rewritten natively. All of that buys a window Safari already
gives us.

**The cost.** The binaries aren't notarized (that takes a paid Apple account),
so the first launch needs "Open Anyway" or `bash install.command`. macOS
guards Desktop, Documents, Downloads and iCloud Drive against background
processes, so keep projects elsewhere.

## One line installs, and finishes the job

**The choice.** `curl -fsSL https://posterm.sh/install | sh` is a POSIX `sh`
script whose code all runs from a `main` called on its last line, so a
download cut short runs nothing. It fetches the release asset for the CPU,
checks it against `SHA256SUMS`, and then does what a newcomer would otherwise
get stuck on:

- installs tmux with whatever package manager is there (apt, dnf, pacman,
  zypper, apk, xbps…), through sudo, after asking;
- puts `~/.local/bin` on the PATH of the user's shell (sh, bash, zsh, fish),
  one marked line per file, removed by `--uninstall`;
- writes `~/.posterm/.env`, which `posterm` reads from any directory that has
  no `.env` of its own;
- runs the hub as a systemd service with `KillMode=process`, a user service
  (with lingering, so logging out doesn't stop it) or a system one as root.
  Without systemd it starts the hub in the background and says it won't come
  back after a reboot;
- prints the URL and the token, plus the SSH tunnel to use when the machine is
  remote.

Questions are read from `/dev/tty`, since stdin is the script itself. With no
terminal (CI, cloud-init), every question takes its default. A
`posterm.service` the script didn't write is never rewritten or removed, and a
hub it didn't start is never killed: it matches its own processes by their
exact command line.

**Why.** "Download a binary" isn't an install when the binary needs tmux, a
PATH entry and something to keep it running. Each of those was a way to lose
someone in the first five minutes.

**What we refused.** Distribution packages (.deb, .rpm, AUR) for now: one
script tested on six distributions covers more ground than one package
maintained badly. Docker: labs need the host's tools, files and logins, and a
container is exactly what they must not live in.

**The cost.** A shell script that edits shell profiles and writes systemd
units has to be tested distribution by distribution, in containers, as a
user, as root and with systemd, and that testing has to be redone after every
change. Distributions whose tmux is older than 3.1 (Ubuntu 20.04) get a clear
"too old" and nothing more.

## What we will not build

- **A unified chat.** You talk to agents in their terminal. That's what
  terminals are for, and every agent's own UI is better at its own job than a
  lowest-common-denominator chat window.
- **An agent loop of our own.** POSTERM runs your agents; it isn't one. No
  planner, no tool layer, no prompts.
- **A wiki as memory.** Agents have their own memory files, and your repo is
  the real memory.
- **POSTERM as a tool for agents.** It's for the human in the loop.
- **Budgets and quotas.** Turns and tokens are the agent's business.

## Rules we keep

- One binary, one port.
- No database. Files and tmux hold everything.
- No JS build step, and no inline script.
- Installation-specific values (name, domain, token, paths, machine names)
  live in `.env`, never in code.
- Labs must survive the hub: restarts, crashes, updates.
- Every user-facing string goes in both dictionaries.
- Nothing gets written or overwritten without the user seeing where.

## Read the code

| file                        | role                                                                         |
| --------------------------- | ---------------------------------------------------------------------------- |
| `src/main.go`               | CLI dispatch, `.env` loading, `posterm state`                                |
| `src/engine.go`             | one machine: registry, tmux, state detection, HTTP API, terminals, uploads   |
| `src/hub.go`                | auth, static wall, spoke tunnels, SSE, journal, Web Push                     |
| `src/user.go`               | privilege drop, tmux socket ownership check                                  |
| `src/secrets.go`            | tokens and VAPID keys: root-only, read before the drop, kept in memory       |
| `src/spokes.go`             | per-machine join tokens: `posterm spoke`, hashes in `spokes.json`            |
| `src/audit.go`              | login budget, journal of the operator's actions                              |
| `src/i18n.go`               | language selection, `T()`, `E()`                                             |
| `src/web/app.js`            | the wall, terminals, selection menu, drops and the browser-side zip          |
| `src/web/sw.js`             | service worker: notifications and inline reply                               |
| `src/web/i18n/*.json`       | every string the program says                                                |
| `config/`                   | systemd unit, user creation script, locked-down Caddyfile                    |
| `macos/`                    | the Mac package: build, install, uninstall                                   |
