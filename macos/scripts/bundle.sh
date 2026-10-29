#!/bin/bash
# Build, assemble and sign "Vani Apply.app" into macos/build/. Installs nothing.
#   /Users/bharath/Code/apps/Vani/macos/scripts/bundle.sh
# VANI_SIGN_IDENTITY overrides the signing identity (default: the Apple
# Development certificate). Signing order is inside-out: appex, host, app.
# VANI_BUNDLE_OUT (an absolute folder) puts the app there instead of macos/build/, for a
# verification run that must not replace the app the installer picks up.
# VANI_REUSE_EXTENSION=1 packs the extension/dist-* folders as they are instead of
# rebuilding them (when extension/src is being edited elsewhere); they must exist.
set -euo pipefail

MACOS="$(cd "$(dirname "$0")/.." && pwd)"
VANI="$(dirname "$MACOS")"
IDENTITY="${VANI_SIGN_IDENTITY:-Apple Development}"
CONFIG="${VANI_BUILD_CONFIG:-release}"
OUT="${VANI_BUNDLE_OUT:-$MACOS/build}"
case "$OUT" in /*) ;; *) echo "VANI_BUNDLE_OUT must be an absolute path: $OUT" >&2; exit 1 ;; esac

swift build -c "$CONFIG" --package-path "$MACOS"
BIN="$(swift build -c "$CONFIG" --package-path "$MACOS" --show-bin-path)"
if [ "${VANI_REUSE_EXTENSION:-}" = "1" ]; then
  [ -d "$VANI/extension/dist-safari" ] && [ -d "$VANI/extension/dist-chrome" ] || { echo "VANI_REUSE_EXTENSION=1 needs extension/dist-safari and dist-chrome; run bun run extension/build.ts" >&2; exit 1; }
else
  bun run "$VANI/extension/build.ts"
fi

# Assemble and sign in a staging folder; the previous app is replaced only once
# the new one is complete and verified, so a failed step leaves it working.
FINAL="$OUT/Vani Apply.app"
# A folder of its own per run: two runs at once must not share one, and Finder
# may drop a .DS_Store into it.
mkdir -p "$OUT"
STAGE="$(mktemp -d "$OUT/.stage.XXXXXX")"
trap 'rm -rf "$STAGE"' EXIT
APP="$STAGE/Vani Apply.app"
APPEX="$APP/Contents/PlugIns/Vani Apply Extension.appex"
mkdir -p "$APP/Contents/MacOS" "$APP/Contents/Helpers" "$APP/Contents/Resources" "$APPEX/Contents/MacOS" "$APPEX/Contents/Resources"

cp "$MACOS/Support/App-Info.plist" "$APP/Contents/Info.plist"
printf 'APPL????' > "$APP/Contents/PkgInfo"
cp "$MACOS/Support/AppIcon.icns" "$APP/Contents/Resources/AppIcon.icns"
cp "$BIN/VaniApply" "$APP/Contents/MacOS/VaniApply"
# The on-device (Apple Intelligence) decision helper; vani (internal/decide/fmbackend) runs it per form field.
cp "$BIN/vani-fm" "$APP/Contents/MacOS/vani-fm"
cp "$BIN/vani-apply-host" "$APP/Contents/Helpers/vani-apply-host"
# The login-item agent (SMAppService) that runs vani jobs for Safari and Chrome.
cp "$BIN/vani-apply-agent" "$APP/Contents/Helpers/vani-apply-agent"
mkdir -p "$APP/Contents/Library/LaunchAgents"
cp "$MACOS/Support/LaunchAgents/dev.vbcr.vani.apply.agent.plist" "$APP/Contents/Library/LaunchAgents/"
# The opt-in login item that keeps `vani daemon` running (Pipeline > Run on schedule). It is
# only registered when the owner turns that on; shipping its plist installs nothing.
cp "$BIN/vani-schedule-agent" "$APP/Contents/Helpers/vani-schedule-agent"
cp "$MACOS/Support/LaunchAgents/dev.vbcr.vani.schedule.agent.plist" "$APP/Contents/Library/LaunchAgents/"
# Vani's Go CLI, for `vani draft` (Claude answer drafts) from the app and the Chrome host.
# VANI_CLI_BINARY reuses an already-built vani (an absolute path) when only the
# Mac or extension side changed and the Go tree is mid-edit elsewhere.
if [ -n "${VANI_CLI_BINARY:-}" ]; then
  [ -x "$VANI_CLI_BINARY" ] || { echo "VANI_CLI_BINARY is not an executable: $VANI_CLI_BINARY" >&2; exit 1; }
  cp "$VANI_CLI_BINARY" "$APP/Contents/Helpers/vani"
else
  # Embedding-based knowledge-base matching (internal/semantic) links the Tessl
  # engine, so it is built in only on Apple silicon when the engine archive
  # exists (`make -C go engine` in the tessl repo). Without it vani still
  # builds and reports "semantic matching not run".
  # The archive sits where go.mod's tessl/go replace points; TESSL_ARCHIVE
  # overrides it (a worktree, another checkout).
  TESSL_GO_DIR="$VANI/../../research/tessl/go"
  if [ -z "${TESSL_ARCHIVE:-}" ] && [ "$(uname -s)/$(uname -m)" = "Darwin/arm64" ] && [ ! -f "$TESSL_GO_DIR/engine/target/release/libgusset.a" ] && [ -f "$TESSL_GO_DIR/Makefile" ]; then
    make -C "$TESSL_GO_DIR" engine
  fi
  TESSL_ARCHIVE="${TESSL_ARCHIVE:-$TESSL_GO_DIR/engine/target/release/libgusset.a}"
  GO_TAGS=""
  TESSL_CFLAGS=""
  if [ "$(uname -s)/$(uname -m)" = "Darwin/arm64" ] && [ -f "$TESSL_ARCHIVE" ]; then
    GO_TAGS="tessl"
    TESSL_SHA="$(shasum -a 256 "$TESSL_ARCHIVE" | cut -c1-16)"
    TESSL_CFLAGS="-DTESSL_ENGINE_SHA=${TESSL_SHA}"
  else
    echo "bundle.sh: building vani without -tags tessl (no Apple silicon or no $TESSL_ARCHIVE): semantic matching will report \"not run\"" >&2
  fi
  # VANI_VERSION (scripts/release.sh) stamps `vani version`; unset, it says "dev".
  CGO_CFLAGS="${TESSL_CFLAGS:+$TESSL_CFLAGS }${CGO_CFLAGS:-}" go -C "$VANI" build ${GO_TAGS:+-tags "$GO_TAGS"} ${VANI_VERSION:+-ldflags "-X main.version=$VANI_VERSION"} -trimpath -o "$APP/Contents/Helpers/vani" ./cmd/vani
fi
cp "$MACOS/Support/Extension-Info.plist" "$APPEX/Contents/Info.plist"
cp "$BIN/VaniApplyExtension" "$APPEX/Contents/MacOS/VaniApplyExtension"
cp -R "$VANI/extension/dist-safari/." "$APPEX/Contents/Resources/"
# The unpacked Chrome extension, for "Load unpacked" (Browsers > Show Extension Folder).
cp -R "$VANI/extension/dist-chrome" "$APP/Contents/Resources/chrome-extension"

sign() { codesign --force --options runtime --timestamp=none --sign "$IDENTITY" "$@"; }
sign --entitlements "$MACOS/Support/Extension.entitlements" "$APPEX"
sign --entitlements "$MACOS/Support/Group.entitlements" --identifier dev.vbcr.vani.apply.host "$APP/Contents/Helpers/vani-apply-host"
sign --entitlements "$MACOS/Support/Group.entitlements" --identifier dev.vbcr.vani.apply.agent "$APP/Contents/Helpers/vani-apply-agent"
sign --entitlements "$MACOS/Support/Group.entitlements" --identifier dev.vbcr.vani.schedule.agent "$APP/Contents/Helpers/vani-schedule-agent"
sign --identifier dev.vbcr.vani.cli "$APP/Contents/Helpers/vani"
sign --identifier dev.vbcr.vani.fm "$APP/Contents/MacOS/vani-fm"
sign --entitlements "$MACOS/Support/Group.entitlements" "$APP"
codesign --verify --deep --strict "$APP"
# vani finds the on-device helper as ../MacOS/vani-fm from the folder it runs in
# (internal/decide/fmbackend), and the host and agent find it the same way
# (FMHelper.bundled): refuse a bundle where that path is not an executable file.
[ -f "$APP/Contents/Helpers/../MacOS/vani-fm" ] && [ -x "$APP/Contents/Helpers/../MacOS/vani-fm" ] || { echo "bundle.sh: vani-fm is not at Contents/MacOS/vani-fm" >&2; exit 1; }
rm -rf "$FINAL"
mv "$APP" "$FINAL"
echo "built and signed: $FINAL"
