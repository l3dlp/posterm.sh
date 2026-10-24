#!/usr/bin/env bash
# posterm.sh — publish site/ to where the web server reads it. Run from anywhere:
#
#   site/deploy.sh
#
# The target is POSTERM_SITE_DIR (environment, or the repo's .env): the
# directory the web server serves for posterm.sh. Editing site/ changes
# nothing live until this runs. Files are copied with rsync -c, all at once
# (--delay-updates), never deleted on the other side; the generator (gen/) and
# the build scripts stay here. `install` must arrive byte for byte: it is
# checked after the copy.
set -euo pipefail

root=$(cd "$(dirname "$0")/.." && pwd)
dest=${POSTERM_SITE_DIR:-}
if [ -z "$dest" ] && [ -f "$root/.env" ]; then
	dest=$(sed -n 's/^POSTERM_SITE_DIR=//p' "$root/.env" | tr -d "\"'" | tail -1)
fi
[ -n "$dest" ] || { echo "set POSTERM_SITE_DIR (environment or .env): where posterm.sh is served from" >&2; exit 1; }
[ -d "$dest" ] || { echo "$dest does not exist" >&2; exit 1; }
case $dest in /) echo "refusing to deploy to /" >&2; exit 1 ;; esac

say() { printf '\033[1m→\033[0m %s\n' "$*"; }
say "site/ → $dest"
rsync -rc --delay-updates --exclude gen/ --exclude build.sh --exclude deploy.sh "$root/site/" "$dest/"
cmp -s "$root/site/install" "$dest/install" || { echo "install differs after the copy" >&2; exit 1; }

# Keep the target's owner on what we wrote (a web app may need to rewrite it).
owner=$(stat -c '%u:%g' "$dest")
[ "$(id -u)" != 0 ] || chown -R "$owner" "$dest"
say "done"
