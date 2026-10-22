#!/bin/bash
# Build, sign and (unless --dry-run) publish a private GitHub release of Vani.
#   scripts/release.sh vMAJOR.MINOR.PATCH [--dry-run] [--skip-tests]
# Policy (docs/RELEASE.md): signed with the Apple Development identity exactly as
# macos/scripts/bundle.sh signs, not notarized, published as a GitHub release in
# the private repo, rolled back by reinstalling the previous release's zip.
#
# --dry-run builds, signs and verifies into dist/ and reports every precondition
#   that would refuse a real release; it never tags, pushes or creates a release.
# --skip-tests skips `go test` and `swift test`; the release notes say so loudly.
#
# Test seams (tests only; never set them for a real release):
#   VANI_RELEASE_GIT  git executable (default: git)
#   VANI_RELEASE_GH   gh executable  (default: gh)
set -euo pipefail

GIT="${VANI_RELEASE_GIT:-git}"
GH="${VANI_RELEASE_GH:-gh}"

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
DIST="$ROOT/dist"
PLIST="$ROOT/macos/Support/App-Info.plist"
APP_NAME="Vani Apply.app"
REPO="bharathvbcr/vani"

die() { echo "release.sh: refusing: $*" >&2; exit 1; }

DRY=0
SKIP_TESTS=0
VERSION=""
for arg in "$@"; do
  case "$arg" in
    --dry-run) DRY=1 ;;
    --skip-tests) SKIP_TESTS=1 ;;
    -*) die "unknown flag $arg (usage: scripts/release.sh vMAJOR.MINOR.PATCH [--dry-run] [--skip-tests])" ;;
    *) [ -z "$VERSION" ] || die "more than one VERSION given"; VERSION="$arg" ;;
  esac
done
[ -n "$VERSION" ] || die "VERSION missing (usage: scripts/release.sh vMAJOR.MINOR.PATCH [--dry-run] [--skip-tests])"

# Preconditions that always refuse, dry run or not.
[[ "$VERSION" =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]] \
  || die "version-format: '$VERSION' is not vMAJOR.MINOR.PATCH"
PLIST_VERSION="$(/usr/libexec/PlistBuddy -c 'Print :CFBundleShortVersionString' "$PLIST" 2>/dev/null)" \
  || die "plist-unreadable: cannot read CFBundleShortVersionString from $PLIST"
[ "v$PLIST_VERSION" = "$VERSION" ] \
  || die "version-mismatch: $VERSION given but CFBundleShortVersionString is $PLIST_VERSION (bump $PLIST first)"
[ -z "${VANI_CLI_BINARY:-}" ] \
  || die "vani-cli-override: VANI_CLI_BINARY is set; a release embeds the stamped vani it builds itself"

IS_PRIVATE="$("$GH" repo view "$REPO" --json isPrivate --jq .isPrivate 2>/dev/null)" \
  || die "repo-visibility-unknown: could not ask gh whether $REPO is private"
[ "$IS_PRIVATE" = "true" ] || die "repo-not-private: $REPO reports isPrivate=$IS_PRIVATE; this policy publishes to a private repo only"

# Preconditions on git state: refuse for a real release, report in a dry run.
NOTES=()
problem() {
  if [ "$DRY" = 1 ]; then
    echo "release.sh: dry-run would refuse: $*" >&2
    NOTES+=("$*")
  else
    die "$*"
  fi
}

HEAD_SHA="$("$GIT" -C "$ROOT" rev-parse HEAD)"
[ -z "$("$GIT" -C "$ROOT" status --porcelain)" ] || problem "dirty-tree: working tree has uncommitted changes"
BRANCH="$("$GIT" -C "$ROOT" branch --show-current)"
[ "$BRANCH" = "main" ] || problem "not-on-main: HEAD is on '${BRANCH:-detached}'"
REMOTE_MAIN="$("$GIT" -C "$ROOT" ls-remote origin refs/heads/main | cut -f1)" \
  || problem "origin-unreadable: could not read origin/main"
if [ -z "$REMOTE_MAIN" ]; then
  problem "origin-main-missing: origin has no main branch"
elif [ "$REMOTE_MAIN" != "$HEAD_SHA" ]; then
  problem "head-not-origin-main: HEAD ${HEAD_SHA:0:7} != origin/main ${REMOTE_MAIN:0:7} (push main first)"
fi
if "$GIT" -C "$ROOT" rev-parse -q --verify "refs/tags/$VERSION" >/dev/null; then
  problem "tag-exists-locally: $VERSION"
fi
[ -z "$("$GIT" -C "$ROOT" ls-remote --tags origin "refs/tags/$VERSION")" ] || problem "tag-exists-on-origin: $VERSION"

if [ "$SKIP_TESTS" = 1 ]; then
  echo "release.sh: WARNING tests skipped (--skip-tests); the release notes will say so" >&2
  NOTES+=("tests-skipped")
else
  go -C "$ROOT" test ./... || die "go-tests-failed"
  swift test --package-path "$ROOT/macos" || die "swift-tests-failed"
fi

# Build: bundle.sh signs inside-out and stamps the embedded vani with VANI_VERSION.
rm -rf "$DIST"
mkdir -p "$DIST"
VANI_VERSION="$VERSION" "$ROOT/macos/scripts/bundle.sh"
APP="$ROOT/macos/build/$APP_NAME"
codesign --verify --deep --strict "$APP" || die "codesign-verify-failed: $APP"

CLI="$APP/Contents/Helpers/vani"
GOT="$("$CLI" version)" || die "cli-version-failed: $CLI version"
[ "$GOT" = "vani $VERSION" ] || die "cli-version-mismatch: embedded vani reports '$GOT'"

APP_ZIP="Vani-Apply-$VERSION.zip"
CLI_ZIP="vani-$VERSION-darwin-arm64.zip"
ditto -c -k --keepParent "$APP" "$DIST/$APP_ZIP"
CLI_STAGE="$DIST/.cli-stage"
mkdir -p "$CLI_STAGE"
cp "$CLI" "$CLI_STAGE/vani"
ditto -c -k "$CLI_STAGE" "$DIST/$CLI_ZIP"
rm -rf "$CLI_STAGE"
(cd "$DIST" && shasum -a 256 "$APP_ZIP" "$CLI_ZIP" > SHA256SUMS)

# Signing summary: team id and identity kind only.
SIGN_INFO="$(codesign -dv --verbose=2 "$APP" 2>&1)"
TEAM="$(printf '%s\n' "$SIGN_INFO" | sed -n 's/^TeamIdentifier=//p' | head -n1)"
KIND="$(printf '%s\n' "$SIGN_INFO" | sed -n 's/^Authority=\([^:]*\):.*/\1/p' | head -n1)"
SUMMARY="${KIND:-unknown identity}, team ${TEAM:-unknown}, hardened runtime"

NOTES_FILE="$DIST/.release-notes.md"
{
  echo "# Vani $VERSION"
  echo
  echo "- Commit: $HEAD_SHA"
  echo "- Signing: $SUMMARY. **Not notarized.**"
  case " ${NOTES[*]:-} " in
    *tests-skipped*) echo "- **TESTS WERE SKIPPED for this release (--skip-tests).**" ;;
  esac
  echo
  echo "## First open"
  echo "Gatekeeper will not recognise the signature. Right-click the app, choose Open, then Open again."
  echo
  echo "## Install"
  echo "1. Download $APP_ZIP and $CLI_ZIP, and verify with \`shasum -a 256 -c SHA256SUMS\`."
  echo "2. Quit Vani Apply, unzip, and move \`$APP_NAME\` to /Applications (replace the old one)."
  echo "3. Unzip the CLI and put \`vani\` on your PATH (for example /usr/local/bin/vani)."
  echo
  echo "## Roll back"
  echo "Download the previous release's zips, quit Vani Apply, replace /Applications/$APP_NAME with the"
  echo "previous app, replace the vani binary likewise, and reopen. See docs/RELEASE.md."
} > "$NOTES_FILE"

echo
echo "built $VERSION at ${HEAD_SHA:0:7}: $SUMMARY"
ls -l "$DIST/$APP_ZIP" "$DIST/$CLI_ZIP" "$DIST/SHA256SUMS"

if [ "$DRY" = 1 ]; then
  echo "dry run: nothing tagged, pushed or published. Notes: $NOTES_FILE"
  if [ "${#NOTES[@]}" -gt 0 ]; then
    echo "a real release would be refused for: ${NOTES[*]}"
  fi
  exit 0
fi

"$GIT" -C "$ROOT" tag -a "$VERSION" -m "Vani $VERSION" "$HEAD_SHA"
"$GIT" -C "$ROOT" push origin "refs/tags/$VERSION"
"$GH" release create "$VERSION" "$DIST/$APP_ZIP" "$DIST/$CLI_ZIP" "$DIST/SHA256SUMS" \
  --repo "$REPO" --title "Vani $VERSION" --notes-file "$NOTES_FILE"
echo "published $VERSION to $REPO"
