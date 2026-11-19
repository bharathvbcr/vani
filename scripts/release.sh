#!/bin/bash
# Build, sign and (unless --dry-run) publish a private GitHub release of Vani.
#   scripts/release.sh vMAJOR.MINOR.PATCH [--dry-run] [--skip-tests] [--allow-no-tessl]
# Policy (docs/RELEASE.md): signed with the Apple Development identity exactly as
# macos/scripts/bundle.sh signs, not notarized, published as a GitHub release in
# the private repo, rolled back by reinstalling the previous release's zip.
#
# --dry-run builds, signs and verifies into dist/ and reports every precondition
#   that would refuse a real release; it never tags, pushes or creates a release.
# --skip-tests skips `go test` and `swift test`; the release notes say so loudly.
# --allow-no-tessl lets a release embed a vani built without -tags tessl (no semantic matching); without the flag
#   such a build is refused, and with it the release notes say so.
#
# The tag must name the tree that was built. The build reads the live working tree, so after it (immediately before
# tagging) the script re-verifies that HEAD is still HEAD_SHA, that no tracked or untracked file appeared or changed
# (gitignored build outputs stay ignored), and that go.work and go.work.sum, the ignored files the Go build reads, hash
# as they did before the build; it refuses otherwise. The other ignored inputs are not compared: the extension's dist
# folders and macos/.build are outputs the build itself regenerates, and the Tessl archive lives outside the repo
# (bundle.sh records its hash in the engine stamp).
# (Chosen over building in a detached worktree of HEAD_SHA: the build needs ignored local state - go.work, the
# extension's dist folders, the Tessl archive - that a fresh worktree does not carry.)
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
ALLOW_NO_TESSL=0
VERSION=""
for arg in "$@"; do
  case "$arg" in
    --dry-run) DRY=1 ;;
    --skip-tests) SKIP_TESTS=1 ;;
    --allow-no-tessl) ALLOW_NO_TESSL=1 ;;
    -*) die "unknown flag $arg (usage: scripts/release.sh vMAJOR.MINOR.PATCH [--dry-run] [--skip-tests] [--allow-no-tessl])" ;;
    *) [ -z "$VERSION" ] || die "more than one VERSION given"; VERSION="$arg" ;;
  esac
done
[ -n "$VERSION" ] || die "VERSION missing (usage: scripts/release.sh vMAJOR.MINOR.PATCH [--dry-run] [--skip-tests] [--allow-no-tessl])"

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
SWIFT_SKIPPED=""
problem() {
  if [ "$DRY" = 1 ]; then
    echo "release.sh: dry-run would refuse: $*" >&2
    NOTES+=("$*")
  else
    die "$*"
  fi
}

# Ignored files the Go build reads (go.work points the build at sibling checkouts); git cannot see them change.
build_inputs() {
  local f
  for f in go.work go.work.sum; do
    if [ -f "$ROOT/$f" ]; then shasum -a 256 "$ROOT/$f"; else echo "absent $f"; fi
  done
}

HEAD_SHA="$("$GIT" -C "$ROOT" rev-parse HEAD)"
INPUTS_BEFORE="$(build_inputs)"
# Every emptiness test on a command's output below captures that output on its own line first: a failed command prints
# nothing, and testing "$(cmd)" inline would read its failure as a clean result.
TREE_STATUS="$("$GIT" -C "$ROOT" status --porcelain)" || problem "git-status-failed: could not read the working tree state"
[ -z "$TREE_STATUS" ] || problem "dirty-tree: working tree has uncommitted changes"
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
REMOTE_TAG="$("$GIT" -C "$ROOT" ls-remote --tags origin "refs/tags/$VERSION")" || problem "tag-lookup-failed: could not ask origin whether $VERSION exists"
[ -z "$REMOTE_TAG" ] || problem "tag-exists-on-origin: $VERSION"

if [ "$SKIP_TESTS" = 1 ]; then
  echo "release.sh: WARNING tests skipped (--skip-tests); the release notes will say so" >&2
  NOTES+=("tests-skipped")
else
  # One runner for every gate (scripts/test.sh): Go, Swift, the extension's Bun tests and its type-check run
  # concurrently, uncached (--fresh), with the Jobs tab UI tests (--ui), and each suite's exit code is left in the
  # run directory.
  RUNDIR="$ROOT/tmp/test-runs"
  # --ui: a release is run in the foreground at the owner's terminal, where the window tests may take focus.
  if ! "$ROOT/scripts/test.sh" --fresh --ui; then
    LAST="$RUNDIR/latest"
    [ "$(cat "$LAST/go.rc" 2>/dev/null || echo 1)" = 0 ] || die "go-tests-failed: see $LAST/go.log"
    [ "$(cat "$LAST/swift.rc" 2>/dev/null || echo 1)" = 0 ] || die "swift-tests-failed: see $LAST/swift.log and swift-serial.log"
    [ "$(cat "$LAST/bun.rc" 2>/dev/null || echo 1)" = 0 ] || die "extension-tests-failed: see $LAST/bun.log"
    [ "$(cat "$LAST/types.rc" 2>/dev/null || echo 1)" = 0 ] || die "typecheck-failed: see $LAST/types.log"
    die "tests-failed: scripts/test.sh exited non-zero with every suite passing (see $LAST)"
  fi
  if [ -f "$RUNDIR/latest/bun.rerun" ]; then
    echo "release.sh: WARNING $(cat "$RUNDIR/latest/bun.rerun") extension test(s) failed under --parallel and passed only on a serial re-run (see $RUNDIR/latest/bun.log)" >&2
  fi
  # A Swift test that skipped (no Go vani built, runner not trusted for accessibility) is not a test that ran.
  if [ -f "$RUNDIR/latest/swift.skipped" ]; then
    SWIFT_SKIPPED="$(cat "$RUNDIR/latest/swift.skipped")"
    echo "release.sh: WARNING $(cat "$RUNDIR/latest/swift.skipped") Swift tests skipped (reasons: $RUNDIR/latest/swift-skips.txt); the release notes say so" >&2
  fi
fi

# Build: bundle.sh signs inside-out and stamps the embedded vani with VANI_VERSION.
rm -rf "$DIST"
mkdir -p "$DIST"
BUILD_INFO="$DIST/.build-info"
VANI_BUILD_INFO_OUT="$BUILD_INFO" VANI_VERSION="$VERSION" "$ROOT/macos/scripts/bundle.sh"
# The embedding engine must be in the vani that ships: bundle.sh records whether it was (a build without it only warns).
NO_TESSL=0
TESSL_STATE="$(sed -n 's/^tessl=//p' "$BUILD_INFO" 2>/dev/null | head -n1 || true)"
if [ "$TESSL_STATE" != "1" ]; then
  NO_TESSL=1
  if [ "$ALLOW_NO_TESSL" = 1 ]; then
    echo "release.sh: WARNING vani built without -tags tessl (--allow-no-tessl); the release notes say so" >&2
  else
    problem "no-tessl: the embedded vani was not built with -tags tessl (build info: '${TESSL_STATE:-missing}'); this build is refused unless --allow-no-tessl is passed (or build the engine archive)"
  fi
fi
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
  [ "$NO_TESSL" = 0 ] || echo "- **Built WITHOUT the Tessl embedding engine** (--allow-no-tessl): semantic knowledge-base matching reports \"not run\"."
  [ -z "$SWIFT_SKIPPED" ] || echo "- **$SWIFT_SKIPPED Swift tests skipped, not run** (no Go vani built, or the runner not trusted for accessibility)."
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

# The tag below names HEAD_SHA, so the artifacts above must have been built from exactly that tree: HEAD must not have
# moved, no tracked file may differ from it, no untracked file may have appeared (the build's own outputs are
# gitignored), and the ignored files the build reads must be unchanged.
[ "$("$GIT" -C "$ROOT" rev-parse HEAD)" = "$HEAD_SHA" ] || problem "head-moved: HEAD changed from ${HEAD_SHA:0:7} during the build; the artifacts do not match it"
TREE_STATUS="$("$GIT" -C "$ROOT" status --porcelain)" || problem "git-status-failed: could not re-read the working tree state after the build"
[ -z "$TREE_STATUS" ] || problem "tree-changed: a file changed or appeared during the build; the artifacts do not match ${HEAD_SHA:0:7}"
INPUTS_AFTER="$(build_inputs)"
[ "$INPUTS_AFTER" = "$INPUTS_BEFORE" ] || problem "build-inputs-changed: go.work or go.work.sum changed during the build; the artifacts do not match ${HEAD_SHA:0:7}"

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
