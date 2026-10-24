#!/usr/bin/env bash
# POSTERM — rebuild ./posterm, cleanly.
#
#   bin/build.sh
#
# - checks first (gofmt, go vet, go test);
# - builds next to it (posterm.new): if that fails, the old binary stays put;
# - static binary (CGO_ENABLED=0): copy it as is onto a spoke;
# - if the result differs, keeps the old one in bin/backup/ under a timestamped
#   name, then swaps with an atomic rename: a running hub keeps its binary in
#   memory until it restarts;
# - keeps only the last 7 days of backups;
# - an install from before the rename still runs ./monia: it becomes a symlink
#   to ./posterm, so its service and hooks keep working;
# - restarts the posterm (or pre-rename monia) systemd service, if there is one.
set -euo pipefail

root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"
backups=bin/backup
keep_days=7

exec 9>bin/.build.lock
flock -n 9 || { echo "a build is already running" >&2; exit 1; }

command -v go >/dev/null || { echo "go not found" >&2; exit 1; }
say() { printf '\033[1m→\033[0m %s\n' "$*"; }

unformatted=$("$(go env GOROOT)/bin/gofmt" -l src) # gofmt is not always on the PATH
[ -z "$unformatted" ] || { echo "not gofmt'd: $unformatted" >&2; exit 1; }
go vet ./...
go test ./src

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
		cp -p "$old" "$saved"
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
[ -n "$changed" ] || exit 0

if [ -d "$backups" ]; then
	find "$backups" -maxdepth 1 -type f \( -name 'posterm.*' -o -name 'monia.*' \) -mtime +$((keep_days - 1)) -print -delete |
		sed 's/^/  deleted (older than '$keep_days' days): /'
fi

say "posterm: $(du -h posterm | cut -f1), sha256 $(sha256sum posterm | cut -c1-16)…"
for unit in posterm monia; do
	if systemctl cat "$unit" >/dev/null 2>&1; then
		systemctl restart "$unit"
		say "systemctl restart $unit"
		break
	fi
done
