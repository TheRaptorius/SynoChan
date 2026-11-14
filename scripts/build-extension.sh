#!/usr/bin/env bash
# Packs the browser extension as a zip for Chromium based browsers (Chrome, Edge, Brave) ("Load unpacked" after unzipping, or the Web Store).
#
# Usage: scripts/build-extension.sh <version>
#   VERSION  e.g. 1.0.0 (a leading "v" and any "-build" suffix are stripped; browsers want plain numbers)
# Environment:
#   OUT_DIR  output directory (default dist)
set -euo pipefail

VERSION="${1:?usage: build-extension.sh <version>}"
VERSION="${VERSION#v}"
VERSION="${VERSION%%-*}"
OUT_DIR="${OUT_DIR:-dist}"
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

if ! [[ "$VERSION" =~ ^[0-9]+(\.[0-9]+){0,3}$ ]]; then
    echo "invalid extension version: $VERSION" >&2
    exit 1
fi

cp -R "$ROOT/extension/." "$WORK/"
python3 - "$WORK/manifest.json" "$VERSION" <<'PY'
import json, sys
path, version = sys.argv[1], sys.argv[2]
with open(path) as f:
    manifest = json.load(f)
manifest["version"] = version
with open(path, "w") as f:
    json.dump(manifest, f, indent=2)
    f.write("\n")
PY
cp "$ROOT/LICENSE" "$WORK/LICENSE"

mkdir -p "$OUT_DIR"
# Built for Chromium based browsers (Chrome, Edge, Brave, …).
OUT="$(cd "$OUT_DIR" && pwd)/synochan-extension-chrome-$VERSION.zip"
rm -f "$OUT"
(cd "$WORK" && zip -qr -X "$OUT" .)
echo "Wrote $OUT"
