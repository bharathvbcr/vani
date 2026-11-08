# Releasing Vani

Decided by the owner on 2026-10-22 (card ft-52b6e7d3f0950c24195d9c20c0a20351).

## Policy

The source repository was made public on 2026-11-07. The release script was not changed
with it, so `scripts/release.sh` still refuses while `isPrivate` is false. A GitHub release
of the app is not cut until this check is deliberately changed.

- **Private release.** Releases were GitHub releases in the then-private repo `bharathvbcr/vani`. The
  script refuses to publish if `gh repo view --json isPrivate` is not `true`.
- **Signing.** The Apple Development identity, exactly as `macos/scripts/bundle.sh` signs (team
  `HAUHVD8M37`, hardened runtime, inside-out). `VANI_SIGN_IDENTITY` still overrides.
- **No notarization.** Nothing is submitted to Apple and nothing is stapled. Gatekeeper therefore does
  not recognise the app: on first open, right-click `Vani Apply.app`, choose Open, then Open again.
  (Downloads carry a quarantine flag; the right-click Open is what clears it for this app.)
- **Assets.** `Vani-Apply-vX.Y.Z.zip` (the signed app, made with `ditto`), `vani-vX.Y.Z-darwin-arm64.zip`
  (the same stamped, signed `vani` binary the app embeds) and `SHA256SUMS`. No SBOM, no workflow, no
  updater: no client checks for updates, so publishing offers nothing to anyone automatically.
- **Version.** `vMAJOR.MINOR.PATCH`, equal to `CFBundleShortVersionString` in
  `macos/Support/App-Info.plist`; `vani version` prints it (`dev` for an unstamped build).

## Cut a release

1. Bump the marketing version (`App-Info.plist`, `Extension-Info.plist`, `extension/static/manifest.json`)
   and merge to `main`. **Push `main` yourself**: the script requires HEAD to equal `origin/main`.
2. Rehearse. This builds, signs and verifies into `dist/` and reports what would refuse; it never tags,
   pushes or publishes:

   ```bash
   /Users/bharath/Code/apps/Vani/scripts/release.sh v0.1.0 --dry-run
   ```

3. Publish. This runs `go test ./...` and `swift test --package-path macos`, builds, verifies with
   `codesign --verify --deep --strict`, creates an annotated tag, pushes only that tag, and runs
   `gh release create`:

   ```bash
   /Users/bharath/Code/apps/Vani/scripts/release.sh v0.1.0
   ```

Refusals, each named in the message: `version-format`, `version-mismatch`, `repo-not-private`,
`dirty-tree`, `not-on-main`, `head-not-origin-main`, `tag-exists-locally`, `tag-exists-on-origin`,
`go-tests-failed`, `swift-tests-failed`, `codesign-verify-failed`. `--skip-tests` skips both test runs
and says so in the release notes. In `--dry-run` the git-state refusals are reported, not enforced.

## Install

1. Download the two zips and `SHA256SUMS` from the release (`gh release download vX.Y.Z --repo
   bharathvbcr/vani`), then `shasum -a 256 -c SHA256SUMS`.
2. Quit Vani Apply. Unzip the app zip and replace `/Applications/Vani Apply.app` with it.
3. Unzip the CLI zip and place `vani` on your PATH, replacing the old one.
4. Open the app (right-click, Open the first time).
5. To keep the daily schedule (nothing else runs `vani daemon`): open the Pipeline tab, turn on **Run
   on schedule**, and approve "Vani Apply" in System Settings ▸ General ▸ Login Items & Extensions. The
   schedule agent runs the bundled `vani daemon` with the decision key read from your login Keychain
   (never written to a file); `vani status` stops saying "schedule not kept" once the daemon announces
   itself. The Keychain may ask once to let the new helper read `openrouter-api-key`: allow it. On every
   later install, turn the switch off and on so the running daemon becomes the new `vani`
   (`macos/README.md` item 6a).

## Roll back

Rolling back is reinstalling the previous release.

1. Download the previous release's zips (`gh release download vPREVIOUS --repo bharathvbcr/vani`) and
   verify them against its `SHA256SUMS`.
2. Quit Vani Apply, replace `/Applications/Vani Apply.app` with the previous app, replace the `vani`
   binary likewise, reopen. If Run on schedule was on, turn it off in the new app first (a release
   from before the schedule agent has no switch to turn it off and would leave the helper registered).
3. If the bad release should not be installed again, delete it and its tag:
   `gh release delete vX.Y.Z --repo bharathvbcr/vani --yes --cleanup-tag`.

State (config, ledger) is not part of the app and is untouched by either direction.

## Test seams

`scripts/release.sh` reads `VANI_RELEASE_GIT` and `VANI_RELEASE_GH` (executables) so its tests
(`cmd/vani/release_script_test.go`) never reach the network. Do not set them for a real release.
