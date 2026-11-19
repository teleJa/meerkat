#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")"
ROOT="$(cd ../.. && pwd)"

# Web UI
mkdir -p frontend/dist
cp "$ROOT/web/dist/index.html" frontend/dist/index.html

# App binary (includes Go Hub + embedded UI)
go build -tags production -o bin/meerkatdesktop .

# App bundle: no Node runtime, no bundled Node Center.
rm -rf bin/Meerkat.app
mkdir -p bin/Meerkat.app/Contents/MacOS bin/Meerkat.app/Contents/Resources
cp bin/meerkatdesktop bin/Meerkat.app/Contents/MacOS/meerkatdesktop
cp build/darwin/Info.plist bin/Meerkat.app/Contents/Info.plist
plutil -replace CFBundleExecutable -string meerkatdesktop bin/Meerkat.app/Contents/Info.plist
plutil -replace CFBundleName -string Meerkat bin/Meerkat.app/Contents/Info.plist
plutil -replace CFBundleDisplayName -string Meerkat bin/Meerkat.app/Contents/Info.plist
plutil -replace CFBundleIdentifier -string com.meerkat.desktop bin/Meerkat.app/Contents/Info.plist
plutil -replace CFBundleShortVersionString -string 0.1.0 bin/Meerkat.app/Contents/Info.plist
plutil -replace CFBundleVersion -string 0.1.0 bin/Meerkat.app/Contents/Info.plist
plutil -remove LSUIElement bin/Meerkat.app/Contents/Info.plist >/dev/null 2>&1 || true
plutil -remove CFBundleIconName bin/Meerkat.app/Contents/Info.plist >/dev/null 2>&1 || true
cp build/darwin/icons.icns bin/Meerkat.app/Contents/Resources/icons.icns
cp "$ROOT/edge/bin/meerkat-edge-darwin-arm64" bin/Meerkat.app/Contents/MacOS/meerkat-edge
codesign --force --deep --sign - bin/Meerkat.app >/dev/null 2>&1 || true
codesign --verify --deep --strict bin/Meerkat.app 2>&1 || true
du -sh bin/Meerkat.app