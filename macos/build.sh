#!/usr/bin/env bash
# POSTERM for macOS: build the newbie package, from Linux or from a Mac.
#
#   macos/build.sh   ->   macos/dist/POSTERM-macos.zip
#
# The zip holds both static darwin binaries (Apple silicon and Intel), the
# installer, the uninstaller and the guide. Nothing here touches ./posterm, the
# running hub or bin/backup/.
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
root=$(dirname "$here")
pkg=$here/dist/POSTERM
zip=$here/dist/POSTERM-macos.zip

command -v go >/dev/null || { echo "go not found" >&2; exit 1; }
command -v zip >/dev/null || { echo "zip not found" >&2; exit 1; }
say() { printf '\033[1m→\033[0m %s\n' "$*"; }

rm -rf "$pkg" "$zip"
mkdir -p "$pkg/bin"
for arch in arm64 amd64; do
	say "darwin/$arch ($(go env GOVERSION))"
	CGO_ENABLED=0 GOOS=darwin GOARCH=$arch go build -C "$root" -trimpath -buildvcs=false -ldflags='-s -w' \
		-o "$pkg/bin/posterm-darwin-$arch" ./src
done
cp "$here/install.command" "$here/uninstall.command" "$here/README.md" "$pkg/"
chmod 755 "$pkg"/*.command "$pkg"/bin/*

(cd "$here/dist" && zip -qry "$zip" POSTERM)
say "$zip: $(du -h "$zip" | cut -f1)"
