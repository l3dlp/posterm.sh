#!/bin/bash
# POSTERM for macOS: install, or update in place (running labs survive).
#
#   double-click it in Finder, or: bash install.command
#
# What it does, in order:
#   1. checks macOS >= 12 and picks the binary for this CPU (Apple silicon/Intel);
#   2. installs tmux with Homebrew if it's missing (and Homebrew if you say so);
#   3. puts the binary in ~/.local/bin/posterm (unquarantined, ad-hoc signed);
#   4. writes ~/.posterm/.env once (language follows macOS), never overwrites it;
#   5. registers a LaunchAgent: the hub starts at login and restarts on crash;
#   6. opens the wall in Safari with the token in your clipboard.
#
# Written for the bash 3.2 that ships with macOS: no bash 4 features.
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
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
die() {
	printf '\n\033[31mxx  %s\033[0m\n' "$*" >&2
	pause
	exit 1
}
# ask_yes "question": default yes.
ask_yes() {
	local a
	read -r -p "    $1 [Y/n] " a || true
	case $a in [nN]*) return 1 ;; esac
}

[ "$(uname -s)" = Darwin ] || die "This is the macOS installer. Elsewhere, follow the main README."
major=$(sw_vers -productVersion | cut -d. -f1)
[ "$major" -ge 12 ] || die "macOS $(sw_vers -productVersion) is too old: POSTERM needs macOS 12 or later."

if [ "$(sysctl -n hw.optional.arm64 2>/dev/null || echo 0)" = 1 ]; then
	arch=arm64 # Apple silicon, even from a Terminal running under Rosetta
else
	arch=amd64
fi
# The user's login shell, and the profile it reads in every new Terminal.
shell=$(dscl . -read "/Users/$(id -un)" UserShell 2>/dev/null | awk '{print $2}' || true)
shell=${shell:-/bin/zsh}
case $shell in
*/zsh) rc=$HOME/.zprofile ;;
*/bash) rc=$HOME/.bash_profile ;;
*) rc= ;;
esac

src=$here/bin/posterm-darwin-$arch
[ -f "$src" ] || die "Missing $src. Unzip the whole POSTERM folder, then run this again."

# --- tmux ---------------------------------------------------------------------
say "tmux"
find_brew() {
	command -v brew 2>/dev/null && return
	for b in /opt/homebrew/bin/brew /usr/local/bin/brew; do
		[ -x "$b" ] && echo "$b" && return
	done
	return 0
}
find_tmux() {
	command -v tmux 2>/dev/null && return
	for t in /opt/homebrew/bin/tmux /usr/local/bin/tmux /opt/local/bin/tmux; do
		[ -x "$t" ] && echo "$t" && return
	done
	return 0
}
tmux=$(find_tmux)
if [ -z "$tmux" ]; then
	brew=$(find_brew)
	if [ -z "$brew" ]; then
		ok "tmux is missing, and so is Homebrew (the usual way to get it)."
		ok "Homebrew's own installer will ask for your Mac password."
		ask_yes "Install Homebrew now?" || die "Install tmux yourself (https://brew.sh, then: brew install tmux) and run this again."
		/bin/bash -c "$(curl -fsSL https://raw.githubusercontent.com/Homebrew/install/HEAD/install.sh)"
		brew=$(find_brew)
		[ -n "$brew" ] || die "Homebrew didn't install. See https://brew.sh."
	fi
	# Homebrew's installer only *prints* how to put brew on the PATH. On Apple
	# silicon that leaves brew and tmux out of every new Terminal: do it.
	if [ "$brew" = /opt/homebrew/bin/brew ] && [ -n "$rc" ] && ! grep -qs 'brew shellenv' "$rc"; then
		printf '\neval "$(/opt/homebrew/bin/brew shellenv)" # Homebrew, added by the POSTERM installer\n' >>"$rc"
		ok "added Homebrew to your PATH in $rc"
	fi
	ok "brew install tmux"
	"$brew" install tmux
	tmux=$(find_tmux)
	[ -n "$tmux" ] || die "tmux still not found after brew install."
fi
version=$("$tmux" -V | sed 's/[^0-9.]//g')
case $version in
0.* | 1.* | 2.* | 3.0*) die "tmux $version is too old (3.1 or later needed): brew upgrade tmux" ;;
esac
ok "$tmux ($version)"

# --- stop the running hub (an update): the labs live in tmux and keep going --
if launchctl print "$domain/$label" >/dev/null 2>&1; then
	say "Stopping the running hub (labs keep running)"
	launchctl bootout "$domain/$label" 2>/dev/null || true
	for _ in 1 2 3 4 5 6 7 8 9 10; do
		launchctl print "$domain/$label" >/dev/null 2>&1 || break
		sleep 0.5
	done
fi

# --- binary -------------------------------------------------------------------
say "Installing $bin_dir/posterm ($arch)"
mkdir -p "$bin_dir"
cp "$src" "$bin_dir/posterm.new"
xattr -c "$bin_dir/posterm.new" 2>/dev/null || true # downloaded: drop the quarantine flag
codesign --force --sign - "$bin_dir/posterm.new" >/dev/null 2>&1 ||
	ok "warning: ad-hoc signing failed; macOS may refuse to run the binary."
chmod 755 "$bin_dir/posterm.new"
mv -f "$bin_dir/posterm.new" "$bin_dir/posterm"
ok "$("$bin_dir/posterm" version 2>/dev/null || echo posterm)"

# --- config, written once -----------------------------------------------------
mkdir -p "$data"
chmod 700 "$data"
if [ ! -f "$data/.env" ]; then
	lang=en
	case $(defaults read -g AppleLanguages 2>/dev/null | sed -n 2p | tr -dc 'a-zA-Z-') in fr*) lang=fr ;; esac
	cat >"$data/.env" <<EOF
# POSTERM on this Mac. All settings: the main README, "Configuration".
# After an edit: launchctl kickstart -k $domain/$label
POSTERM_TITLE=POSTERM
# en or fr
POSTERM_LANG=$lang
POSTERM_ADDR=127.0.0.1:8206
EOF
	chmod 600 "$data/.env"
	ok "wrote $data/.env (language: $lang)"
else
	ok "keeping $data/.env"
fi
addr=$(sed -n 's/^POSTERM_ADDR=//p' "$data/.env" | tail -n 1)
addr=${addr:-127.0.0.1:8206}
url=http://$addr
port=${addr##*:}
if lsof -nP -iTCP:"$port" -sTCP:LISTEN >/dev/null 2>&1; then
	lsof -nP -iTCP:"$port" -sTCP:LISTEN | sed 's/^/    /'
	die "Port $port is taken by the program above. Stop it, or pick another port in $data/.env (POSTERM_ADDR), then run this again."
fi

# --- LaunchAgent --------------------------------------------------------------
say "Starting the hub at every login"
mkdir -p "$(dirname "$plist")"
# AbandonProcessGroup: stopping or updating the hub must never take tmux, and
# therefore the labs, down with it. SHELL: labs start through your login shell,
# so they see the same PATH as your Terminal.
cat >"$plist" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>$label</string>
	<key>ProgramArguments</key>
	<array>
		<string>$bin_dir/posterm</string>
	</array>
	<key>WorkingDirectory</key>
	<string>$data</string>
	<key>EnvironmentVariables</key>
	<dict>
		<key>PATH</key>
		<string>$bin_dir:$(dirname "$tmux"):/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin</string>
		<key>SHELL</key>
		<string>$shell</string>
		<key>LANG</key>
		<string>en_US.UTF-8</string>
	</dict>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<true/>
	<key>ThrottleInterval</key>
	<integer>10</integer>
	<key>AbandonProcessGroup</key>
	<true/>
	<key>ProcessType</key>
	<string>Interactive</string>
	<key>StandardOutPath</key>
	<string>$data/hub.log</string>
	<key>StandardErrorPath</key>
	<string>$data/hub.log</string>
</dict>
</plist>
EOF
plutil -lint "$plist" >/dev/null || die "Generated an invalid $plist. Please report it."
launchctl bootstrap "$domain" "$plist" || die "launchctl refused the hub. Log: $data/hub.log"

up=
for _ in $(seq 1 30); do
	code=$(curl -s -o /dev/null -w '%{http_code}' "$url/" || true)
	if [ "$code" != 000 ]; then
		up=1
		break
	fi
	sleep 0.5
done
if [ -z "$up" ]; then
	tail -n 20 "$data/hub.log" 2>/dev/null | sed 's/^/    /'
	die "The hub didn't answer on $url. Its log is above (full log: $data/hub.log)."
fi
ok "$url is up"

# --- `posterm` in your Terminal and inside the labs ----------------------------
if ! "$shell" -lc 'command -v posterm' >/dev/null 2>&1; then
	if [ -n "$rc" ]; then
		printf '\nexport PATH="$HOME/.local/bin:$PATH" # POSTERM\n' >>"$rc"
		ok "added ~/.local/bin to your PATH in $rc (new Terminal windows)"
	else
		ok "add $bin_dir to your PATH to use \`posterm\` in a Terminal"
	fi
fi

# --- done ---------------------------------------------------------------------
copied=
token_file="$data/secrets/token"
[ -s "$token_file" ] || token_file="$data/token" # hubs before 0.1.1
if [ -s "$token_file" ]; then
	pbcopy <"$token_file" && copied=1
fi
open -a Safari "$url" 2>/dev/null || open "$url"

say "POSTERM is running: $url"
if [ -n "$copied" ]; then
	ok "1. Safari just opened it. Your token is in the clipboard: click the field, Cmd+V, Enter."
else
	ok "1. Safari just opened it. Log in with the POSTERM_TOKEN set in $data/.env."
fi
ok "2. Make it an app: Safari menu File > Add to Dock (macOS 14+)."
ok "   The Dock app keeps its own cookies: paste the token once more there."
ok "3. Hit + lab. Keep projects outside Desktop, Documents, Downloads and iCloud"
ok "   (macOS guards those folders), e.g. in ~/code."
ok ""
ok "Token again:  pbcopy < ~/.posterm/secrets/token"
ok "Update:       run this installer again; labs keep running."
ok "Remove:       uninstall.command"
pause
