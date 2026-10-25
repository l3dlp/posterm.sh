#!/usr/bin/env bash
# POSTERM — take over from an older hub service (pre-rename "monia" builds),
# once. Run as root from this checkout:
#
#   bin/reset.sh --dry-run      show what it found and what it would do
#   bin/reset.sh                do it
#   bin/reset.sh --rollback     give the wall back to the old service
#
# What it does:
# 1. finds the old hub: its unit (OLD_UNIT, default moniado), the address it
#    listens on, its title and language, and its data dir (read from the
#    environment of the labs it started; OLD_DATA overrides);
# 2. builds ./posterm if needed (bin/build.sh --quick --no-site --no-restart);
# 3. writes this checkout's .env (backup kept): POSTERM_DATA, POSTERM_ADDR,
#    POSTERM_TITLE, POSTERM_LANG, POSTERM_SERVICE: same data dir, same port,
#    same token (it lives in the data dir), so browsers stay logged in, push
#    subscriptions keep working and the reverse proxy needs no change;
# 4. installs the NEW_UNIT (default posterm) systemd unit from
#    config/posterm.service, pointed at this checkout;
# 5. makes the old unit restartable (a drop-in fixing its paths, if its
#    directory moved), so that rolling back works;
# 6. stops and disables the old unit, enables and starts the new one. Both
#    have KillMode=process: the tmux server and every lab keep running, the
#    hub is all that changes;
# 7. checks the new hub answers and the labs are all still there; if not, it
#    rolls back on its own.
#
# The labs keep the identity they had (the old hub ran them as root, so does
# this one): changing it means stopping them, so it's left for later (see the
# end of the output).
set -euo pipefail

root=$(cd "$(dirname "$0")/.." && pwd -P)
cd "$root"
OLD_UNIT=${OLD_UNIT:-moniado}
NEW_UNIT=${NEW_UNIT:-posterm}
mode=run
case ${1:-} in
"") ;;
--dry-run) mode=dry ;;
--rollback) mode=rollback ;;
*) echo "usage: bin/reset.sh [--dry-run | --rollback]" >&2; exit 2 ;;
esac

say() { printf '\033[1m→\033[0m %s\n' "$*"; }
ok() { printf '  %s\n' "$*"; }
die() { printf '\033[31mxx %s\033[0m\n' "$*" >&2; exit 1; }
run() { if [ "$mode" = dry ]; then ok "would: $*"; else "$@"; fi; }
unitfile=/etc/systemd/system/$NEW_UNIT.service
dropin=/etc/systemd/system/$OLD_UNIT.service.d/reset-paths.conf

[ "$(id -u)" = 0 ] || die "run it as root (it installs a systemd unit)"
command -v systemctl >/dev/null || die "systemd is needed"

# ---------------------------------------------------------------- rollback

if [ "$mode" = rollback ]; then
	say "rolling back to $OLD_UNIT"
	systemctl disable --now "$NEW_UNIT" 2>/dev/null || true
	systemctl enable --now "$OLD_UNIT"
	last=$(ls -1t .env.before-reset.* 2>/dev/null | head -1 || true)
	[ -z "$last" ] || { command cp -f "$last" .env; ok ".env restored from $last"; }
	ok "$OLD_UNIT is back. The data dir is shared: labs, token and journal are as they were."
	exit 0
fi

# ---------------------------------------------------------------- once

if systemctl is-active -q "$NEW_UNIT" 2>/dev/null && ! systemctl is-active -q "$OLD_UNIT" 2>/dev/null; then
	say "already done: $NEW_UNIT runs, $OLD_UNIT doesn't. Nothing to do."
	exit 0
fi

# ---------------------------------------------------------------- the old hub

say "the old hub: $OLD_UNIT"
systemctl cat "$OLD_UNIT" >/dev/null 2>&1 || die "no unit $OLD_UNIT (set OLD_UNIT)"
pid=$(systemctl show -p MainPID --value "$OLD_UNIT")
[ "${pid:-0}" != 0 ] || die "$OLD_UNIT is not running: nothing to take over"
[ "$(systemctl show -p KillMode --value "$OLD_UNIT")" = process ] ||
	die "$OLD_UNIT has no KillMode=process: stopping it would kill every lab. Fix that first."
exe=$(readlink "/proc/$pid/exe")
ok "pid $pid, binary $exe"

addr=${OLD_ADDR:-$(ss -ltnpH 2>/dev/null | awk -v p="pid=$pid," 'index($0, p) {print $4; exit}')}
[ -n "$addr" ] || die "can't find the address $OLD_UNIT listens on (set OLD_ADDR)"
page=$(curl -fsS --max-time 5 "http://$addr/" 2>/dev/null) || die "the old hub doesn't answer on $addr"
title=${POSTERM_TITLE:-$(printf '%s' "$page" | sed -n 's:.*<title>\([^<]*\)</title>.*:\1:p' | head -1)}
lang=${POSTERM_LANG:-$(printf '%s' "$page" | sed -n 's:.*<html lang="\([a-z]*\)".*:\1:p' | head -1)}
ok "listens on $addr, title \"$title\", language ${lang:-en}"

# Its data dir: every lab it started carries it in its environment.
data=${OLD_DATA:-}
if [ -z "$data" ]; then
	candidates=$(for e in /proc/[0-9]*/environ; do
		{ tr '\0' '\n' <"$e"; } 2>/dev/null | sed -n 's/^\(MONIA\|POSTERM\)_DATA=//p'
	done | sed 's:/*$::' | sort -u)
	for d in $candidates; do
		[ -f "$d/labs.json" ] && [ -S "$d/tmux.sock" ] && tmux -S "$d/tmux.sock" ls >/dev/null 2>&1 &&
			found="${found:+$found }$d"
	done
	set -- ${found:-}
	[ $# = 1 ] || die "found ${#}: ${found:-no live data dir}. Set OLD_DATA to the old hub's data dir."
	data=$1
fi
[ -S "$data/tmux.sock" ] || die "$data/tmux.sock: no tmux server there"
sessions=$(tmux -S "$data/tmux.sock" ls 2>/dev/null | wc -l)
server=$(tmux -S "$data/tmux.sock" display-message -p '#{pid}')
owner=$(stat -c %U "/proc/$server")
ok "data dir $data: $sessions tmux sessions, server pid $server ($owner)"
[ "$owner" = root ] || die "the labs run as $owner: set POSTERM_USER=$owner in .env first, then run this again"

# ---------------------------------------------------------------- plan

say "plan"
ok "binary:  $root/posterm"
ok ".env:    POSTERM_DATA=$data POSTERM_ADDR=$addr POSTERM_TITLE=$title POSTERM_LANG=${lang:-en} POSTERM_SERVICE=$NEW_UNIT"
ok "unit:    $unitfile (WorkingDirectory=$root, KillMode=process)"
ok "switch:  stop + disable $OLD_UNIT, enable + start $NEW_UNIT; tmux and the $sessions sessions keep running"

# ---------------------------------------------------------------- build

if [ ! -x posterm ] || [ -n "$(find src -newer posterm -name '*' -type f 2>/dev/null | head -1)" ]; then
	say "build"
	run bin/build.sh --quick --no-site --no-restart
fi

# ---------------------------------------------------------------- .env

say ".env"
setkey() { # setkey KEY VALUE: replace or append in .env
	local tmp
	tmp=$(mktemp)
	{ grep -v "^$1=" .env 2>/dev/null || true; printf '%s=%s\n' "$1" "$2"; } >"$tmp"
	mv "$tmp" .env
}
if [ "$mode" = run ]; then
	[ ! -f .env ] || command cp -p .env ".env.before-reset.$(date +%Y%m%d-%H%M%S)"
	touch .env
	setkey POSTERM_DATA "$data"
	setkey POSTERM_ADDR "$addr"
	setkey POSTERM_TITLE "$title"
	setkey POSTERM_LANG "${lang:-en}"
	setkey POSTERM_SERVICE "$NEW_UNIT"
	chown root:root .env
	chmod 600 .env
	ok "written (root, 600)"
else
	ok "would write the five keys above (backup first)"
fi

# ---------------------------------------------------------------- units

say "systemd"
unit=$(sed -e "s|^WorkingDirectory=.*|WorkingDirectory=$root|" -e "s|^ExecStart=.*|ExecStart=$root/posterm|" config/posterm.service)
printf '%s\n' "$unit" | grep -q '^KillMode=process' || die "config/posterm.service lost KillMode=process"
if [ "$mode" = run ]; then
	printf '%s\n' "$unit" >"$unitfile"
	ok "$unitfile"
else
	ok "would write $unitfile"
fi
oldwd=$(systemctl show -p WorkingDirectory --value "$OLD_UNIT")
oldwd=${oldwd#-}
if [ ! -d "$oldwd" ]; then # moved since it started: keep it restartable, for --rollback
	newwd=$(dirname "$exe")
	if [ "$mode" = run ]; then
		mkdir -p "$(dirname "$dropin")"
		printf '[Service]\nWorkingDirectory=%s\nExecStart=\nExecStart=%s\n' "$newwd" "$exe" >"$dropin"
	fi
	ok "$OLD_UNIT pointed at $oldwd, which is gone: drop-in $dropin → $newwd (for --rollback)"
fi
run systemctl daemon-reload

# ---------------------------------------------------------------- switch

say "switch"
run systemctl stop "$OLD_UNIT"
run systemctl disable "$OLD_UNIT"
run systemctl enable --now "$NEW_UNIT"
[ "$mode" = run ] || { say "dry run: nothing changed"; exit 0; }

back() {
	printf '\033[31mxx %s — rolling back\033[0m\n' "$*" >&2
	journalctl -u "$NEW_UNIT" -n 20 --no-pager >&2 || true
	systemctl disable --now "$NEW_UNIT" || true
	systemctl enable --now "$OLD_UNIT"
	exit 1
}
for _ in $(seq 1 20); do
	code=$(curl -s -o /dev/null -w '%{http_code}' --max-time 2 "http://$addr/" || true)
	[ "$code" = 200 ] && break
	sleep 1
done
[ "$code" = 200 ] || back "$NEW_UNIT doesn't answer on $addr"
systemctl is-active -q "$NEW_UNIT" || back "$NEW_UNIT is not active"
after=$(tmux -S "$data/tmux.sock" ls 2>/dev/null | wc -l)
[ "$after" -ge "$sessions" ] || back "tmux sessions: $sessions before, $after after"
ok "$NEW_UNIT answers on $addr, $after tmux sessions alive"

say "done: $NEW_UNIT has the wall"
cat <<EOF
  - Same data dir, same token, same port: browsers stay logged in, push keeps
    working, the reverse proxy is unchanged. The token moved into
    $data/secrets/ (see the journal: journalctl -u $NEW_UNIT).
  - bin/build.sh now restarts $NEW_UNIT (POSTERM_SERVICE in .env).
  - $OLD_UNIT is disabled, not deleted: bin/reset.sh --rollback gives it the
    wall back. Once you're sure: systemctl disable $OLD_UNIT; rm its unit file.
  - The labs still run as root, like before. To run them as a dedicated user
    (what keeps the hub's secrets out of their reach): README, "Deploying the
    hub". It means stopping the labs once.
EOF
