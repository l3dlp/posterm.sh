# POSTERM on a Mac

Your coding agents, each in its own terminal, all on one wall, running
on your Mac. You don't need a server or Docker, and you won't compile
anything. Five minutes, most of it waiting for Homebrew.

## What you need

- macOS 12 or later (macOS 14+ to get the Dock app), Apple silicon or Intel.
- An internet connection: the installer fetches `tmux`, the one dependency.
- The agents you want to run (`claude`, `codex`…) already working in your
  Terminal. POSTERM starts them the way your Terminal does.

## Install

1. Unzip `POSTERM-macos.zip` (double-click it). You get a `POSTERM` folder.
2. Double-click **`install.command`** in it.

   If macOS says it can't verify the file, that's because it's not signed
   with a paid Apple account. Either open **System Settings → Privacy &
   Security**, scroll down and hit **Open Anyway**, or skip the dialog
   entirely: open **Terminal** (Cmd+Space, type `Terminal`), type `bash `
   (with the space), drag `install.command` into the window, press Enter.
3. Answer its questions. If `tmux` is missing it installs it with
   [Homebrew](https://brew.sh), and installs Homebrew first if you agree
   (that step asks for your Mac password).
4. Safari opens the wall with your **token already in the clipboard**: click
   the field, Cmd+V, Enter.
5. Make it an app: in Safari, **File → Add to Dock**. POSTERM now has its own
   window, its own Dock icon and its own notifications. The Dock app keeps its
   own cookies, so paste the token once more in there.

Done. Hit **+ lab**: a folder, a command, and it's running.

## What got installed where

| What | Where |
|---|---|
| the program | `~/.local/bin/posterm` |
| settings (language, port, title) | `~/.posterm/.env` |
| lab list, journal, token, log | `~/.posterm/` |
| start at login, restart on crash | `~/Library/LaunchAgents/sh.posterm.hub.plist` |
| `posterm` in new Terminal windows | one line in `~/.zprofile`, marked `# POSTERM` |

Nothing outside your home folder, apart from `tmux` and Homebrew.

## Day to day

- **The labs live in tmux, not in the window.** Close the app, quit Safari,
  update POSTERM: the agents keep working. Only a reboot or a logout stops them.
- **A sleeping Mac sleeps its agents too.** Lid closed means paused. For a long
  run on battery, keep the Mac awake (`caffeinate -i` in a Terminal).
- **Keep projects outside Desktop, Documents, Downloads and iCloud Drive.**
  macOS guards those folders, and a lab started from a background program may
  get "Operation not permitted" there. Use something like `~/code`. You can
  also grant **Full Disk Access** to `~/.local/bin/posterm` (System Settings →
  Privacy & Security; in the file picker, Cmd+Shift+G and paste the path). That
  grant has to be redone after each update.
- **Token lost?** `pbcopy < ~/.posterm/secrets/token` puts it back in the clipboard.
- **French?** Set `POSTERM_LANG=fr` in `~/.posterm/.env` (the installer already
  does it if your Mac is in French), then restart the hub (below).
- **Exact state from your agents' hooks:** `posterm state question` /
  `posterm state idle`, see the main README.

## Restart, update, remove

```sh
launchctl kickstart -k gui/$(id -u)/sh.posterm.hub   # restart after editing ~/.posterm/.env
tail -f ~/.posterm/hub.log                           # what the hub says
```

- **Update**: unzip the new version, run its `install.command`. Settings and
  labs stay.
- **Remove**: run `uninstall.command`. It asks before killing running labs
  and before deleting `~/.posterm`. Your project folders are never touched.

## When something's off

| Symptom | Fix |
|---|---|
| "Port 8206 is taken" | Another program uses it. Set `POSTERM_ADDR=127.0.0.1:8216` in `~/.posterm/.env`, run the installer again. |
| A lab says `command not found` | The command isn't on the PATH of your login shell. Check that it works in a **new** Terminal window. |
| The wall shows nothing after a reboot | Wait a few seconds after login. Then `tail ~/.posterm/hub.log`. |
| No notifications | Allow them for the Dock app in System Settings → Notifications, then hit **notify** in the wall. |

## For whoever builds the zip

From the repo root, on Linux or a Mac with Go installed:

```sh
macos/build.sh   # -> macos/dist/POSTERM-macos.zip (both CPUs, installer, this guide)
```

The binaries are the same single `posterm` as everywhere else, cross-compiled
static, nothing Mac-specific in the code. Not signed or notarized: that takes
a paid Apple Developer account, hence the "Open Anyway" step above.
