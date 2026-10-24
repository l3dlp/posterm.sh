#!/bin/bash
# POSTERM for macOS: remove the hub. Asks before touching your labs or data.
#
#   double-click it in Finder, or: bash uninstall.command
set -euo pipefail

label=sh.posterm.hub
bin_dir=$HOME/.local/bin
data=$HOME/.posterm
plist=$HOME/Library/LaunchAgents/$label.plist
domain=gui/$(id -u)

say() { printf '\n\033[1m==> %s\033[0m\n' "$*"; }
ok() { printf '    %s\n' "$*"; }
# Double-clicked, Terminal closes the window when we're done: wait. Run by the
# posterm.sh one-liner, the user's own terminal stays: don't.
pause() { if [ -t 0 ] && [ -z "${POSTERM_FROM_CURL:-}" ]; then read -r -p "Press Enter to close this window. " _ || true; fi; }
# ask_no "question": default no.
ask_no() {
	local a
	read -r -p "    $1 [y/N] " a || true
	case $a in [yY]*) return 0 ;; esac
	return 1
}

[ "$(uname -s)" = Darwin ] || { echo "This is the macOS uninstaller." >&2; exit 1; }

say "Stopping the hub"
launchctl bootout "$domain/$label" 2>/dev/null || true
rm -f "$plist" "$bin_dir/posterm"
ok "removed the LaunchAgent and $bin_dir/posterm"

for rc in "$HOME/.zprofile" "$HOME/.bash_profile"; do
	if [ -f "$rc" ] && grep -q '# POSTERM$' "$rc"; then
		grep -v '# POSTERM$' "$rc" >"$rc.posterm" && cat "$rc.posterm" >"$rc" && rm -f "$rc.posterm"
		ok "removed the PATH line from $rc"
	fi
done

sock=$data/tmux.sock
tmux=$(command -v tmux || ls /opt/homebrew/bin/tmux /usr/local/bin/tmux 2>/dev/null | head -n 1 || true)
if [ -S "$sock" ] && [ -n "$tmux" ] && "$tmux" -S "$sock" ls >/dev/null 2>&1; then
	say "Labs still running"
	"$tmux" -S "$sock" ls -F '    #{session_name}'
	if ask_no "Kill them? Unsaved work in them is lost."; then
		"$tmux" -S "$sock" kill-server
		ok "killed"
	else
		ok "left running. Later: tmux -S $sock kill-server"
	fi
fi

if [ -d "$data" ]; then
	say "Data: $data (lab list, journal, token, settings)"
	if ask_no "Delete it? Your project folders are not in there and stay untouched."; then
		rm -rf "$data"
		ok "deleted"
	else
		ok "kept: a reinstall picks it up again"
	fi
fi

say "Done"
ok "If you added POSTERM to the Dock: drag it out, and delete it from ~/Applications."
ok "tmux and Homebrew stay installed (brew uninstall tmux, if you want)."
pause
