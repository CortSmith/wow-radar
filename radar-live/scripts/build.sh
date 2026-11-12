#!/bin/sh
# Cross-compiles radar-live into dist/ (static binaries, no cgo) and writes SHA256SUMS.
#   radar-db, radar-ingest: linux/amd64 and linux/arm64
#   radar-feed:             windows/amd64 and linux/amd64
#   radar-client:           windows/amd64 (windowed exe, no console) and linux/amd64 (console + browser)
# The deploy/ files are copied next to the binaries so dist/ is everything to scp to the laptop.
# Usage: scripts/build.sh [version]
set -eu

root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"
dist="$root/dist"

version=${1:-}
if [ -z "$version" ]; then
    version=$(git describe --tags --always --dirty 2>/dev/null || true)
    [ -n "$version" ] || version=dev
fi

rm -rf "$dist"
mkdir -p "$dist"

# build NAME GOOS GOARCH [EXTRA_LDFLAGS]
build() {
    ext=""
    [ "$2" = windows ] && ext=".exe"
    echo "building $1 for $2/$3"
    CGO_ENABLED=0 GOOS=$2 GOARCH=$3 go build -trimpath -buildvcs=false \
        -ldflags "${4:-} -s -w -buildid= -X main.version=$version" \
        -o "$dist/$1-$2-$3$ext" "./cmd/$1"
}

build radar-db linux amd64
build radar-db linux arm64
build radar-ingest linux amd64
build radar-ingest linux arm64
build radar-feed windows amd64
build radar-feed linux amd64
build radar-client windows amd64 -H=windowsgui
build radar-client linux amd64

for f in deploy/*; do
    case "$f" in *_test.go) continue ;; esac
    [ -f "$f" ] && cp "$f" "$dist/"
done
# The fail2ban filter and jail go in a sub-folder (used by install.sh --with-fail2ban).
if [ -d deploy/fail2ban ]; then
    mkdir -p "$dist/fail2ban"
    cp deploy/fail2ban/* "$dist/fail2ban/"
fi

if command -v sha256sum >/dev/null 2>&1; then
    hasher="sha256sum --text"
else
    hasher="shasum -a 256"
fi
(
    cd "$dist"
    # Regular files only, recursively (the fail2ban/ sub-folder), paths relative to dist/ as install.sh expects.
    find . -type f ! -name SHA256SUMS | sed 's|^\./||' | LC_ALL=C sort | while read -r name; do
        $hasher "$name"
    done > SHA256SUMS
)

echo
echo "version $version -> $dist"
ls -l "$dist"
