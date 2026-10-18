#!/usr/bin/env bash
# posterm.sh — rebuild the site in place. Run from anywhere:
#
#   site/build.sh
#
# - images: docs/img and docs/x are the sources; site/img gets web-sized copies
#   (ImageMagick when present, plain copies otherwise);
# - pages: site/gen (Go, stdlib only) writes index.html, fr/index.html and
#   design/index.html from site/gen/*.html and docs/design.md;
# - og.png: re-rendered from site/gen/og.html when Playwright is reachable
#   (PLAYWRIGHT=path/to/playwright, CHROME=path/to/chrome), kept otherwise.
#
# Deploy: everything under site/ except gen/ and build.sh is static. No server
# config needed; /install is served as a plain file.
set -euo pipefail

root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"
say() { printf '\033[1m→\033[0m %s\n' "$*"; }

mkdir -p site/img
# img <src> <dst> [crop WxH+X+Y] <width>
img() {
	local src=$1 dst=$2 crop=${4:+$3} width=${4:-$3}
	if command -v convert >/dev/null; then
		convert "$src" ${crop:+-crop "$crop" +repage} -resize "${width}x>" -strip -define png:compression-level=9 "site/img/$dst"
	else
		cp -f "$src" "site/img/$dst"
	fi
}
say "images"
img docs/img/wall.png wall.png 2880x1060+0+0 1920
img docs/img/wall-fr.png wall-fr.png 2880x1060+0+0 1920
img docs/img/lab.png lab.png 1920
img docs/img/drop.png drop.png 1500x450+900+1255 1200  # the bash and its drop overlay
img docs/img/phone.png phone.png 1170x1760+0+0 780
img docs/x/x-04-arch.png arch.png 1600

say "pages"
go run ./site/gen

if node -e "require(process.env.PLAYWRIGHT || 'playwright')" 2>/dev/null; then
	say "og.png"
	node site/gen/og.cjs
else
	say "og.png kept (no Playwright: set PLAYWRIGHT=… to re-render)"
fi
say "done: site/ is ready to deploy"
