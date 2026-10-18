#!/usr/bin/env bash
# POSTERM — build the release assets that https://posterm.sh/install downloads.
#
#   bin/release.sh v0.1.0
#
# Writes dist/<version>/:
#   posterm-linux-amd64  posterm-linux-arm64      static binaries
#   posterm-darwin-amd64 posterm-darwin-arm64
#   POSTERM-macos.zip                             the Mac package (macos/build.sh)
#   SHA256SUMS                                    checked by the installer
# then prints the `gh release create` line. It publishes nothing by itself, and
# never touches ./posterm or the running hub.
set -euo pipefail

version=${1:?usage: bin/release.sh vX.Y.Z}
case $version in v[0-9]*.[0-9]*.[0-9]*) ;; *) echo "version must look like v1.2.3" >&2; exit 1 ;; esac

root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"
out=dist/$version
command -v go >/dev/null || { echo "go not found" >&2; exit 1; }
say() { printf '\033[1m→\033[0m %s\n' "$*"; }

unformatted=$("$(go env GOROOT)/bin/gofmt" -l src)
[ -z "$unformatted" ] || { echo "not gofmt'd: $unformatted" >&2; exit 1; }
go vet ./...
go test ./src

rm -rf "$out"
mkdir -p "$out"
for target in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64; do
	os=${target%/*} arch=${target#*/}
	say "$os/$arch"
	CGO_ENABLED=0 GOOS=$os GOARCH=$arch go build -trimpath -buildvcs=false -ldflags='-s -w' -o "$out/posterm-$os-$arch" ./src
done
macos/build.sh >/dev/null
cp macos/dist/POSTERM-macos.zip "$out/"
(cd "$out" && sha256sum posterm-* POSTERM-macos.zip >SHA256SUMS)

say "$out:"
(cd "$out" && ls -lh | tail -n +2 | awk '{print "  " $5 "\t" $9}')
cat <<EOF

Pushing the tag is enough: .github/workflows/release.yml rebuilds and publishes.
  git tag $version && git push origin $version
By hand instead (needs gh):
  gh release create $version $out/* --title "POSTERM $version" --generate-notes
EOF
