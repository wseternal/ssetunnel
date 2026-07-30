#!/bin/sh
# Cross-compile ssetunnel for all supported platforms.
# Runs inside the ssetunnel-cross Docker container.
#
# Usage:
#   docker run --rm -v "$PWD":/src -e VERSION=v1.0.0 ssetunnel-cross .docker/build.sh
set -eu

VERSION="${VERSION:-dev}"
GIT_SHA="${GIT_SHA:-$(git rev-parse --short HEAD 2>/dev/null || echo unknown)}"
LDFLAGS="-s -w -X main.Version=${VERSION} -X main.GitSHA=${GIT_SHA}"
BIN=ssetunnel
DIST=/src/dist

mkdir -p "$DIST"

# ── helpers ────────────────────────────────────────────────────────────

build_target() {
  local goos=$1 goarch=$2 cc=$3 suffix=$4
  echo ">> Building ${goos}/${goarch} (CGO=1, CC=${cc})"
  CGO_ENABLED=1 GOOS="$goos" GOARCH="$goarch" CC="$cc" \
    go build -ldflags "$LDFLAGS" \
    -o "${DIST}/${BIN}-${goos}-${goarch}${suffix}" \
    ./cmd/ssetunnel
}

# ── Linux (musl — fully static) ───────────────────────────────────────

build_target linux   amd64 musl-gcc              ""
build_target linux   arm64 aarch64-linux-musl-gcc ""

# ── Windows ────────────────────────────────────────────────────────────

build_target windows amd64 x86_64-w64-mingw32-gcc ".exe"

# ── macOS (osxcross — only if toolchain is present) ────────────────────

if [ -x /opt/osxcross/target/bin/o64-clang ]; then
  build_target darwin amd64 /opt/osxcross/target/bin/o64-clang  ""
  build_target darwin arm64 /opt/osxcross/target/bin/oa64-clang ""
else
  echo ">> Skipping darwin targets (osxcross not available)"
fi

# ── checksums ──────────────────────────────────────────────────────────

echo ">> Generating checksums"
cd "$DIST"
sha256sum * > checksums.txt
echo ">> Release build complete"
