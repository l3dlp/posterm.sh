#!/usr/bin/env bash
# POSTERM — check everything, rebuild ./posterm, rebuild and publish the site,
# restart the hub. One command, safe to re-run:
#
#   bin/build.sh                 everything
#   bin/build.sh --quick         skip the race detector and the fuzzers
#   bin/build.sh --no-site       leave posterm.sh alone
#   bin/build.sh --no-restart    build and deploy, but don't touch the service
#
# Checks, in order (any failure stops everything, nothing is replaced):
#   gofmt (src, site/gen) · go vet · go test -race · a short fuzzing run of the
#   confinement boundary · sh/bash syntax of every script · the installer embeds
#   the release key of site/allowed_signers.
# Then:
# - builds next to it (posterm.new): if that fails, the old binary stays put;
#   static (CGO_ENABLED=0): copy it as is onto a spoke;
# - if the result differs, keeps the old one in bin/backup/ (7 days), then
#   swaps with an atomic rename: a running hub keeps its binary in memory until
#   it restarts;
# - an install from before the rename still runs ./monia: it becomes a symlink
#   to ./posterm, so its service and hooks keep working;
# - site: site/build.sh regenerates the pages from docs/design.md and the
#   templates, site/deploy.sh publishes them to POSTERM_SITE_DIR (skipped, with
#   a note, when that isn't set);
# - restarts the hub's systemd service when the binary changed: the unit named
#   by POSTERM_SERVICE, or else the one whose WorkingDirectory is this checkout.
#   Never a unit just because of its name.
set -euo pipefail

quick= site=1 restart=1
for arg in "$@"; do
	case $arg in
	--quick) quick=1 ;;
	--no-site) site= ;;
	--no-restart) restart= ;;
	*) echo "usage: bin/build.sh [--quick] [--no-site] [--no-restart]" >&2; exit 2 ;;
	esac
done

root=$(cd "$(dirname "$0")/.." && pwd -P)
cd "$root"
backups=bin/backup
keep_days=7

exec 9>bin/.build.lock
flock -n 9 || { echo "a build is already running" >&2; exit 1; }

command -v go >/dev/null || { echo "go not found" >&2; exit 1; }
say() { printf '\033[1m→\033[0m %s\n' "$*"; }
die() { printf '\033[31mxx %s\033[0m\n' "$*" >&2; exit 1; }
envval() { # envval KEY: from the environment, else from ./.env
	local v=${!1:-}
	[ -n "$v" ] || [ ! -f .env ] || v=$(sed -n "s/^$1=//p" .env | tr -d "\"'" | tail -1)
	printf '%s' "$v"
}

# ---------------------------------------------------------------- checks

say "checks"
unformatted=$("$(go env GOROOT)/bin/gofmt" -l src site/gen) # gofmt is not always on the PATH
[ -z "$unformatted" ] || die "not gofmt'd: $unformatted"
go vet ./...
if [ -z "$quick" ] && command -v gcc >/dev/null; then
	CGO_ENABLED=1 go test -race -count=1 ./src # the race detector needs cgo
else
	[ -n "$quick" ] || say "no C compiler: tests without the race detector"
	go test -count=1 ./src
fi
if [ -z "$quick" ]; then
	for f in FuzzInside FuzzUnpack FuzzReceive; do
		go test ./src -run '^$' -fuzz "^$f\$" -fuzztime "${FUZZTIME:-10s}" >/dev/null ||
			die "$f found something: see src/testdata/fuzz/$f"
	done
	say "fuzzers: ${FUZZTIME:-10s} each, nothing found"
fi
sh -n site/install || die "site/install: syntax"
for s in bin/*.sh site/*.sh macos/*.sh macos/*.command; do
	[ -f "$s" ] || continue
	bash -n "$s" || die "$s: syntax"
done
grep -qF "$(cut -d' ' -f2- site/allowed_signers)" site/install ||
	die "site/install does not embed the key in site/allowed_signers"

# ---------------------------------------------------------------- site

if [ -n "$site" ]; then
	say "site: regenerate"
	site/build.sh >/dev/null
fi

# ---------------------------------------------------------------- binary

say "building ($(go env GOVERSION))"
rm -f posterm.new
trap 'rm -f posterm.new' EXIT
CGO_ENABLED=0 go build -trimpath -buildvcs=false -ldflags="-s -w -X main.version=$(git describe --tags --always 2>/dev/null || echo dev)" -o posterm.new ./src

old=posterm
[ -f "$old" ] || [ ! -f monia ] || [ -L monia ] || old=monia # first build after the rename
changed=1
if [ -f "$old" ]; then
	if cmp -s "$old" posterm.new; then
		say "same as the installed binary: nothing to replace"
		changed=
	else
		mkdir -p "$backups"
		saved="$backups/$old.$(date +%Y%m%d-%H%M%S)"
		command cp -p "$old" "$saved"
		touch "$saved" # a backup's age counts from today, not from its build
		chown --reference="$old" posterm.new 2>/dev/null || true
		say "old binary saved: $saved"
	fi
fi
if [ -n "$changed" ]; then
	chmod 755 posterm.new
	mv -f posterm.new posterm
fi
rm -f posterm.new
trap - EXIT
if [ -e monia ] && [ ! -L monia ]; then
	ln -sfn posterm monia.link && mv -Tf monia.link monia
	say "./monia is now a symlink to ./posterm (pre-rename service and hooks)"
fi
if [ -d "$backups" ]; then
	find "$backups" -maxdepth 1 -type f \( -name 'posterm.*' -o -name 'monia.*' \) -mtime +$((keep_days - 1)) -print -delete |
		sed 's/^/  deleted (older than '$keep_days' days): /'
fi
say "posterm: $(du -h posterm | cut -f1), sha256 $(sha256sum posterm | cut -c1-16)…"

# ---------------------------------------------------------------- deploy

if [ -n "$site" ]; then
	if [ -n "$(envval POSTERM_SITE_DIR)" ]; then
		POSTERM_SITE_DIR=$(envval POSTERM_SITE_DIR) site/deploy.sh
	else
		say "site not deployed: POSTERM_SITE_DIR is not set (environment or .env)"
	fi
fi

# ---------------------------------------------------------------- restart

[ -n "$changed" ] || exit 0
[ -n "$restart" ] || { say "service not restarted (--no-restart)"; exit 0; }
command -v systemctl >/dev/null || exit 0

# The unit to restart: POSTERM_SERVICE, or the one running from this checkout.
# A unit merely called posterm or monia may be something else entirely.
unit=$(envval POSTERM_SERVICE)
if [ -z "$unit" ]; then
	for u in $(systemctl list-unit-files --type=service --no-legend 2>/dev/null | awk '{print $1}'); do
		wd=$(systemctl show -p WorkingDirectory --value "$u" 2>/dev/null)
		wd=${wd#-} wd=${wd#!} # systemd's "optional" and "as root" prefixes
		[ -n "$wd" ] && [ -d "$wd" ] || continue
		if [ "$(cd "$wd" && pwd -P)" = "$root" ]; then
			unit=$u
			break
		fi
	done
fi
if [ -z "$unit" ]; then
	say "no systemd unit runs from $root: nothing restarted (set POSTERM_SERVICE to name one)"
	exit 0
fi
[ "$(systemctl show -p KillMode --value "$unit")" = process ] ||
	die "$unit has no KillMode=process: restarting it would kill every lab. Fix the unit first."
systemctl restart "$unit"
say "systemctl restart $unit"
