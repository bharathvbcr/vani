#!/bin/bash
# Build, assemble and sign "Vani Apply.app" into macos/build/. Installs nothing.
#   /Users/bharath/Code/apps/Vani/macos/scripts/bundle.sh
# VANI_SIGN_IDENTITY overrides the signing identity (default: the Apple
# Development certificate). Signing order is inside-out: appex, host, app.
set -euo pipefail

MACOS="$(cd "$(dirname "$0")/.." && pwd)"
VANI="$(dirname "$MACOS")"
IDENTITY="${VANI_SIGN_IDENTITY:-Apple Development}"
CONFIG="${VANI_BUILD_CONFIG:-release}"

swift build -c "$CONFIG" --package-path "$MACOS"
BIN="$(swift build -c "$CONFIG" --package-path "$MACOS" --show-bin-path)"
bun run "$VANI/extension/build.ts"

# Assemble and sign in a staging folder; the previous app is replaced only once
# the new one is complete and verified, so a failed step leaves it working.
FINAL="$MACOS/build/Vani Apply.app"
STAGE="$MACOS/build/.stage"
APP="$STAGE/Vani Apply.app"
APPEX="$APP/Contents/PlugIns/Vani Apply Extension.appex"
rm -rf "$STAGE"
mkdir -p "$APP/Contents/MacOS" "$APP/Contents/Helpers" "$APP/Contents/Resources" "$APPEX/Contents/MacOS" "$APPEX/Contents/Resources"

cp "$MACOS/Support/App-Info.plist" "$APP/Contents/Info.plist"
printf 'APPL????' > "$APP/Contents/PkgInfo"
cp "$MACOS/Support/AppIcon.icns" "$APP/Contents/Resources/AppIcon.icns"
cp "$BIN/VaniApply" "$APP/Contents/MacOS/VaniApply"
cp "$BIN/vani-apply-host" "$APP/Contents/Helpers/vani-apply-host"
# The login-item agent (SMAppService) that runs vani jobs for Safari and Chrome.
cp "$BIN/vani-apply-agent" "$APP/Contents/Helpers/vani-apply-agent"
mkdir -p "$APP/Contents/Library/LaunchAgents"
cp "$MACOS/Support/LaunchAgents/dev.vbcr.vani.apply.agent.plist" "$APP/Contents/Library/LaunchAgents/"
# Vani's Go CLI, for `vani draft` (Claude answer drafts) from the app and the Chrome host.
go -C "$VANI" build -trimpath -o "$APP/Contents/Helpers/vani" ./cmd/vani
cp "$MACOS/Support/Extension-Info.plist" "$APPEX/Contents/Info.plist"
cp "$BIN/VaniApplyExtension" "$APPEX/Contents/MacOS/VaniApplyExtension"
cp -R "$VANI/extension/dist-safari/." "$APPEX/Contents/Resources/"
# The unpacked Chrome extension, for "Load unpacked" (Browsers > Show Extension Folder).
cp -R "$VANI/extension/dist-chrome" "$APP/Contents/Resources/chrome-extension"

sign() { codesign --force --options runtime --timestamp=none --sign "$IDENTITY" "$@"; }
sign --entitlements "$MACOS/Support/Extension.entitlements" "$APPEX"
sign --entitlements "$MACOS/Support/Group.entitlements" --identifier dev.vbcr.vani.apply.host "$APP/Contents/Helpers/vani-apply-host"
sign --entitlements "$MACOS/Support/Group.entitlements" --identifier dev.vbcr.vani.apply.agent "$APP/Contents/Helpers/vani-apply-agent"
sign --identifier dev.vbcr.vani.cli "$APP/Contents/Helpers/vani"
sign --entitlements "$MACOS/Support/Group.entitlements" "$APP"
codesign --verify --deep --strict "$APP"
rm -rf "$FINAL"
mv "$APP" "$FINAL"
rmdir "$STAGE"
echo "built and signed: $FINAL"
