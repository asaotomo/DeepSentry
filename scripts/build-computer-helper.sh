#!/usr/bin/env bash
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
OUT="${1:-$ROOT/build/computer-use-helper}"
mkdir -p "$(dirname "$OUT")"
STAGE="$(mktemp -d "${TMPDIR:-/tmp}/deepsentry-computer-helper.XXXXXX")"
trap 'rm -rf "$STAGE"' EXIT
SDK="$(xcrun --sdk macosx --show-sdk-path)"
for ARCH in arm64 x86_64; do
 xcrun swiftc -O -sdk "$SDK" -target "${ARCH}-apple-macos12.0" \
  "$ROOT/internal/desktop/helpers/macos.swift" -o "$STAGE/$ARCH" \
  -framework AppKit -framework ApplicationServices
done
lipo -create "$STAGE/arm64" "$STAGE/x86_64" -output "$OUT"
# Production distribution should sign with Developer ID and notarize the bundle.
codesign --force --sign - --identifier com.hx0studio.deepsentry.computer-use "$OUT"
printf 'Built universal macOS helper: %s\n' "$OUT"
