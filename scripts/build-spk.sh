#!/usr/bin/env bash
# Builds the noarch SynoChan SPK (one package with binaries for x86_64, aarch64, armv7 and i686).
#
# Usage: scripts/build-spk.sh <version> [build-number]
#   VERSION  e.g. 1.0.0 (a leading "v" is stripped)
#   BUILD    package build number, default 1
# Environment:
#   MAINTAINER  author shown in Package Center and About (default "TheRaptorius")
#   REPO_URL    project page (default https://github.com/TheRaptorius/SynoChan)
#   CHANGELOG   short change log text for Package Center
#   OUT_DIR     output directory (default dist)
set -euo pipefail

VERSION="${1:?usage: build-spk.sh <version> [build-number]}"
VERSION="${VERSION#v}"
BUILD="$(printf '%04d' "${2:-1}")"
SPK_VERSION="${VERSION}-${BUILD}"
MAINTAINER="${MAINTAINER:-TheRaptorius}"
REPO_URL="${REPO_URL:-https://github.com/TheRaptorius/SynoChan}"
CHANGELOG="${CHANGELOG:-}"
OUT_DIR="${OUT_DIR:-dist}"

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
cd "$ROOT"

echo "Building SynoChan $SPK_VERSION"

# 1. Binaries. Static (no cgo), so they run on any DSM 7 kernel/libc.
TARGET="$WORK/target"
mkdir -p "$TARGET/bin"
build() {
    local goarch="$1" name="$2" extra="${3:-}"
    echo "  go build linux/$goarch"
    env CGO_ENABLED=0 GOOS=linux GOARCH="$goarch" $extra \
        go build -trimpath -ldflags "-s -w -X main.version=$SPK_VERSION -X main.repoURL=$REPO_URL -X main.author=$MAINTAINER" \
        -o "$TARGET/bin/synochan-linux-$name" ./cmd/synochan
}
build amd64 amd64
build arm64 arm64
build arm armv7 GOARM=7
build 386 386 GO386=softfloat

# 2. UI served by DSM from /webman/3rdparty/synochan/.
mkdir -p "$TARGET/ui"
cp -R web/static/. "$TARGET/ui/"
cp package/ui/config "$TARGET/ui/config"
install -m 0755 package/ui/api.cgi "$TARGET/ui/app/api.cgi"
cp LICENSE NOTICE "$TARGET/"

# 3. package.tgz
PKGDIR="$WORK/spk"
mkdir -p "$PKGDIR"
tar --owner=0 --group=0 --numeric-owner -C "$TARGET" -czf "$PKGDIR/package.tgz" .

# 4. Package metadata.
cp -R package/scripts package/conf package/WIZARD_UIFILES "$PKGDIR/"
chmod 0755 "$PKGDIR"/scripts/*
cp LICENSE "$PKGDIR/LICENSE"
cp web/static/images/icon_64.png "$PKGDIR/PACKAGE_ICON.PNG"
cp web/static/images/icon_256.png "$PKGDIR/PACKAGE_ICON_256.PNG"

escape() { printf '%s' "$1" | sed -e 's/[\/&|]/\\&/g' -e 's/"/\\"/g' | tr '\n' ' '; }
sed -e "s|@VERSION@|$SPK_VERSION|" \
    -e "s|@MAINTAINER@|$(escape "$MAINTAINER")|g" \
    -e "s|@REPO_URL@|$(escape "$REPO_URL")|g" \
    -e "s|@CHANGELOG@|$(escape "$CHANGELOG")|" \
    package/INFO.in > "$PKGDIR/INFO"
{
    echo "checksum=\"$(md5sum "$PKGDIR/package.tgz" | cut -d' ' -f1)\""
    echo "extractsize=\"$(du -sk "$TARGET" | cut -f1)\""
} >> "$PKGDIR/INFO"

# 5. The SPK is an uncompressed tar.
mkdir -p "$OUT_DIR"
SPK="$OUT_DIR/synochan-$SPK_VERSION-noarch.spk"
tar --owner=0 --group=0 --numeric-owner -C "$PKGDIR" -cf "$SPK" \
    INFO package.tgz scripts conf WIZARD_UIFILES LICENSE PACKAGE_ICON.PNG PACKAGE_ICON_256.PNG
echo "Wrote $SPK ($(du -h "$SPK" | cut -f1))"
