# Release report: v0.1.0

Prepared on 2026-10-21. Nothing was pushed. No tag was created. No release was published.

PUSH was `no`. Phase 6 was skipped.

The race-tested code is `aa8ed0cd35f3d283ec02be1a7acfcc2af1893eb0`. This file and `CHANGELOG.md` land in a later documentation commit. That commit was not race-tested. Tag the commit that introduces `CHANGELOG.md` and `RELEASE_REPORT.md`.

## Orientation

Checked again on 2026-10-21 before writing these files.

`gitpulse_insights` on `/Users/bharath/Code/apps/Vani` scanned 25 worktrees, with 0 unscanned. `main` was clean at `aa8ed0cd35f3d283ec02be1a7acfcc2af1893eb0` (0 dirty files). Four idle Claude sessions share that checkout (pids 1577, 44126, 44411, 49882). `shared_worktree_files` was 0. Overlapping dirty files were `internal/config/config.go`, `internal/config/config_test.go`, and `internal/decide/kbmatch/store.go`, held by `.claude/worktrees/agent-a8480655de18be7f4` and `tmp/wt-jibe`. This release does not commit those files. The ledger facet was stale: newest catch-up `2026-10-21T20:06:40.881Z`, stale after 600s. A stale ledger is not a clean bill.

`git status` was empty. `git rev-list --left-right --count origin/main...HEAD` was `0` behind and `342` ahead. `git rev-list --count HEAD` was 421. `origin/main` is `0dbee8a3ea8ecaff606b603b0a66e27f1de7bb80`. This documentation commit adds one more. Re-count before pushing. `git tag -l` was empty. `git ls-remote --tags origin` was empty.

`ft-aa3086e6` is not on Vani's board (`gitpulse_get_task` returned `not_found`). The card lives on GitPulse. It settles a push order of DevCouncil, then Manvi, then gusset, then GitPulse, and that agents never touch tags. Vani is not in that order. This release followed the tag rule: no tag was created. What the card leaves open for Vani is the whole publish path, filed as `ft-52b6e7d3f0950c24195d9c20c0a20351`.

## Commits made for this release

Already on `main` before this report:

| Commit | Subject |
| --- | --- |
| `205849ff0e726b4cf3733282ede2f1af07674978` | `fix(extension): escape non-ASCII so Safari can parse the bundles` |
| `42c4f64bdeb4fc8e7ddcd62234492c49dba813bd` | `test(rankfit): finish inside the default 10 minute timeout` |
| `1da07b080a6d4c68838b972dd580129e9d2c3cf0` | `test(extension): give the second-facet crawl 30s` |
| `aa8ed0cd35f3d283ec02be1a7acfcc2af1893eb0` | `test(rankfit): count Platt samples without a shared int` |

None of those four names a GitPulse task. `ft-60d1dc8e` covers extension routing and sender policy, which is `ff0ba3862bc4d0322e8f31748a2d44acf8cd6610`, not the Safari encoding fix.

`git diff --stat 205849ff0e726b4cf3733282ede2f1af07674978..aa8ed0cd35f3d283ec02be1a7acfcc2af1893eb0` is rankfit `*_test.go` files and `extension/test/crawler.test.ts` only (14 files, +72/−22). No production Go, no Swift, no extension runtime.

The documentation commit that adds `CHANGELOG.md` and `RELEASE_REPORT.md` is the last commit of this preparation. Its hash is the commit you tag. It is not written into this file.

DevMap impact was used on the rankfit test helpers before those commits. `world`, `stratumWorld`, and `TestAttackDeterminismAcrossRunsAndProcs` affected seven files, all under `internal/rankfit`, with no production callers. The ASCII bundle test drives `extension/build.ts` as a subprocess, so `devmap_affected_tests` returned nothing for that change. These two Markdown files change no symbols.

## Verification

There is no `ci:local`. `rg` over the repo finds the string only in `BRANCH_REPORT.md`, which already records the absence. README "Build and test" (lines 787–788) is the documented check:

```bash
go -C /Users/bharath/Code/apps/Vani vet ./...
go -C /Users/bharath/Code/apps/Vani test -race ./...
```

The extension suite is `bun test` on `extension/test/`. The Mac app is `swift test --package-path macos`. A package or discovery that runs zero tests is listed below and is not a pass.

### Go race suite on `aa8ed0c`

Command, from a clean tree at that commit: `go test -race -count=1 -json ./... > tmp/go-test-3.json; echo EXIT:$?`

The shell printed `EXIT:0`. Wall clock 288.12s (2026-10-21T20:25:28Z to 2026-10-21T20:30:16Z). Re-counted this turn from `tmp/go-test-3.json`:

| Count | Value |
| --- | --- |
| Packages seen | 64 |
| Packages with tests | 59 |
| Packages with zero tests (`action=skip`) | 5 |
| Tests passed | 3462 |
| Tests failed | 0 |
| Tests skipped | 16 |
| Package failures | none |

Zero-test packages, not passes: `build/bigco`, `build/e2eseed`, `build/local-employers`, `build/pacing-probe`, `internal/sources/srctest`.

The 16 skips are opt-in (real ledgers, Lightpanda, `VANI_ATTACK_SLOW`, a built `vani` binary). They are not passes. Rankfit elapsed 146.998s inside this default-timeout run. `cmd/vani` elapsed 285.55s.

`go vet ./...` on `aa8ed0c` was recorded in the previous part of this session as exit 0 with no output. This turn did not re-run it. The documentation commit does not change Go.

### What failed before that green run

The first full `go test -race -count=1 -json ./...` was piped through `tee` and a counter. The wrapper's exit 0 was not a pass: rankfit hit `panic: test timed out after 10m0s`. The counter on that log was 3451 pass, 0 fail, 16 skip, and one package failure (`internal/rankfit`). Alone, with `-timeout 20m`, the same package passed in 799.947s, past the default 10 minute deadline.

After the parallel-test change and before the race was known, `go test -race -count=1 -timeout 10m ./internal/rankfit/` passed in 167.567s. That schedule did not overlap the shared counter. It is not evidence the parallel tests were race-free.

A later full suite was killed after the race detector had already logged failures. That log is not a pass. One data race: `sampleSeq` in `samples()`, stacks in `TestPlattInvalidP` and `TestPlattRefusesBelowThreshold`. Other "race detected during execution of test" lines are that same race attributed to whichever test was running.

After the atomic counter, the package alone passed in 120.683s. The full suite above is the evidence for `aa8ed0c`.

### Extension

`bun test extension/test/` on `aa8ed0c` exited 1: 4147 pass, 5 fail, 4 errors, 157 files, 45.25s. Bun matched the substring `extension/test/` inside other lanes' scratch trees under `tmp/` (`wt-jibe`, `headext`, `notice-commit`, `amd/headcopy`). Those trees were left alone.

`bun test /Users/bharath/Code/apps/Vani/extension/test/` exited 0: 1191 pass, 0 fail, 14823 `expect()` calls, 45 files, 25.08s. That is the extension evidence for `aa8ed0c`, including the 30s crawler test.

### Swift

`swift test --package-path macos` was run at `205849ff0e726b4cf3733282ede2f1af07674978`, before the three test-only commits. Those commits do not touch Swift.

- `VaniApplyUITests`: 53 tests, 12 skipped, 0 failures. The skips say the test runner is not trusted for Accessibility.
- `VaniApplyKitTests`: 544 tests, 7 skipped, 0 failures. Skips include contract tests that want a Go `vani` at `build/vani`.
- Two Swift Testing lines say `Test run with 0 tests in 0 suites`. That is empty discovery. The XCTest suites above are the run.

This turn did not re-run Swift.

## Release pipeline

This host cannot select Sonnet, Opus, or Fable. The audit in the previous part of the session used four read-only code-reader subagents. The rows below are what this session checked in the tree, the saved logs, and the GitPulse cards.

### Version consistency

Verified this turn. The marketing version is already `0.1.0`:

- `extension/static/manifest.json` `"version": "0.1.0"`
- `macos/Support/App-Info.plist` `CFBundleShortVersionString` `0.1.0`, `CFBundleVersion` `1`, `LSMinimumSystemVersion` `14.0`
- `macos/Support/Extension-Info.plist` the same short version, build version, and minimum system

`extension/package.json` has name `vani-apply-extension`, `private: true`, and no version field. This turn confirmed there is no root `package.json`, no `Cargo.toml`, no `Cargo.lock`, and no `tauri.conf.json`. `v0.1.0` does not exist as a local tag or on origin.

`CFBundleVersion` stays `1`. Bumping it was left for the owner.

### Changelog

`CHANGELOG.md` did not exist before this commit. It now has one `[0.1.0]` section. There is no previous tag and no previous section, so there is no orphan tag and no orphan section.

### Workflows

There is no `.github` directory (`test -d .github` failed). There is no `release.yml` to audit for triggers, permissions, `persist-credentials`, SHA pins, install scripts, checksums, SBOM, or signing. Creating one would invent a publish path. That decision is `ft-52b6e7d3f0950c24195d9c20c0a20351`.

### Signing and the bundle

`macos/scripts/bundle.sh` (read this turn) builds the Swift package, runs `extension/build.ts`, and copies `VaniApply`, `vani-apply-host`, `vani-apply-agent`, `VaniApplyExtension`, the Go `vani` CLI, the LaunchAgent plist, Safari `dist-safari` into the appex, and Chrome `dist-chrome` into Resources. It signs inside-out with identity `${VANI_SIGN_IDENTITY:-Apple Development}`, `codesign --force --options runtime --timestamp=none`, then `codesign --verify --deep --strict`, and only then replaces `macos/build/Vani Apply.app`. It does not notarize, staple, or write a checksum or an SBOM. If the host is not Darwin/arm64 or the Tessl archive is missing, it still builds `vani` without `-tags tessl` and warns. There is no Windows or Linux app bundle.

### Update check

A search of `*.{md,json,yml,yaml,go,swift,ts,plist}` for `Sparkle`, `SUFeedURL`, `appcast`, and `CheckForUpdates` found no updater. The only `ci:local` hit is `BRANCH_REPORT.md`. `internal/config/update.go` is the config-file writer (`Update` replaces the config under a lock). No client can be offered v0.1.0 until a client that checks exists, and that check has to wait until a release and its checksums exist. Neither exists today.

### Reproducibility

Verified this turn. `.gitignore` ignores `/go.work` and `/go.work.sum`. `go.mod` requires Manvi, decide, and tessl at `v0.0.0` and replaces decide with `../../devtools/decide` and tessl with `../../research/tessl/go`. Manvi is not a `go.mod` replace. README lines 764–780 tell a fresh clone to write the ignored `go.work` by hand, with absolute paths. `go.sum` is absent (`test -f go.sum` failed) and is not gitignored. A clone without those sibling checkouts cannot build. `go.mod` and `go.work` were not edited. The ask is `ft-8d8ca2ea609144eb5240c2b743487d58`.

### Rollback

No updater offers a build, so there is nothing to withdraw from a client. No tag and no GitHub release exist, so there is nothing to delete. The steps for after a bad publish are an owner decision on `ft-52b6e7d3f0950c24195d9c20c0a20351`, to be written down before the first tag push. This session did not delete any ref.

### Fixed, filed, and still yours

Fixed, in the four commits above: Safari bundle encoding, the rankfit default-timeout failure, the Platt sample-counter race, and the crawler test's 5s timeout. No publish-pipeline file was changed.

Filed, backlog, not implemented here:

- `ft-52b6e7d3f0950c24195d9c20c0a20351` — how v0.1.0 is signed, published, and rolled back. Covers Developer ID versus Apple Development, notarization, timestamps, checksums, SBOM, a workflow, an updater, and rollback text.
- `ft-8d8ca2ea609144eb5240c2b743487d58` — a clean clone cannot build.

Left on its existing card, still `in_progress`, not touched: `ft-22d7da5f2735997b8bbdb7934bc1345c` (`atomicfile.Write` and a symlinked path).

Needs your decision before any tag:

- Whether v0.1.0 is the commit that contains these notes (recommended) and whether you want it tagged at all while origin has no release workflow.
- Signing identity, notarization, checksums, SBOM, and whether a GitHub workflow should exist.
- How a clone is supposed to build (the clean-clone card).
- `CFBundleVersion` is still `1`.
- Vani is outside the GitPulse push order. Pushing `main` publishes the local history that has never been on origin.

Tasks already in `review` from earlier audits were not treated as accepted. The notes cite commits.

## Release notes

The same text is in `CHANGELOG.md`.

### Highlights

- Vani starts as a listings-first career engine: a Go daemon, the macOS app Vani Apply, and the browser extension (`cbfe2caa94a655a18cc4433c7dd87070c7f2c2cc`).
- Source boards search more than one query, with verified site filters and four search-site ATS kinds (`a0bc4f6052dc03be2fc53a462274bd98381fefd2`).
- Jobs ingest keeps an inbox, a near-duplicate check, cross-source identity, and a Jibe source (`cfb7b0ac7ec0b373bfd78f99e264edaba0da6e0d`).
- Jobs insight adds a location filter, an extension apply broker, and shadow eval (`365a40537ab46c57a0c2fe047ce1fe2b4e14213b`).
- The extension counts fill, save, and scan in every frame and answers every message (`c2444cfd84bd7a6dde05061009fbfafd06a78a08`). Basics and question intents fill the first form (`1bbb13ff6174f17334a179966fa111b61369cc78`).
- Jobs and the digest rank by one Score. The legacy relevance and priority fields are gone (`b71f0ebf89bd84bfb8383a7cc1b9f09d1f1d8f4a`).
- An optional embedding match compares postings with the resume knowledge base (`ce06891745cd836d28437dccbfc4f320513818ca`). It links only with `-tags tessl` on Darwin/arm64 when the Tessl archive is present. Otherwise the build still ships and semantic matching reports that it did not run (`macos/scripts/bundle.sh`).
- The daemon slices `waitUntil`, so sleep and wake cannot skip the daily run (`4ad64d001f52c60228fa260a8e203e209a94340c`). The Mac app announces that run with a native notification that opens Jobs (`fe49bf11b199d6b62f179a9a5adb14b08b4b8a8f`).

### Fixes

- Safari reads an extension script with no charset as windows-1252. A literal combining mark in the résumé normalizer then throws "range out of order" and the background script never loads. The bundler writes every non-ASCII UTF-16 unit as `\uXXXX` (`205849ff0e726b4cf3733282ede2f1af07674978`).
- `attachResume` goes to the sender's tab. The extension typechecks from a pinned compiler. A sender policy refuses the wrong caller before work runs. Scraped links that are not `http` or `https` are dropped (`ff0ba3862bc4d0322e8f31748a2d44acf8cd6610`).
- The daemon exits when the ledger is poisoned, and a knowledge-base sync replay keeps its stamp (`b6dd36a2545cd4f78609be42615576cc9a2cdc57`).
- Fallback title keys keep C++, C#, and .NET apart (`7a6ee3e2da5a0fb9f5cbdc06724bd2009e510731`).
- Test only. Rankfit's race tests now finish inside Go's default 10 minute package deadline (`42c4f64bdeb4fc8e7ddcd62234492c49dba813bd`). On their own, before this change, they took 799.947s.
- Test only. Platt sample keys use an atomic counter, so parallel tests do not share one increment (`aa8ed0cd35f3d283ec02be1a7acfcc2af1893eb0`).
- Test only. The second-facet crawl allows 30s. A quiet run is about 2.7s; Bun's 5s default failed it while a Go race suite shared the machine (`1da07b080a6d4c68838b972dd580129e9d2c3cf0`).

### Hardening

- Append-only stores fsync the directory on create, guard a torn tail, cap note lines, bound a partial knowledge-base sync, refuse rekey on Windows, and bound the spool (`317beb38867f4fb3a54807909d59f51c6d8f85df`).
- The Go tree compiles for Windows and Linux, child processes share one environment list, and the vault allowlist is sealed (`333f62abdc3d64a4221c9cb642ac05edef32407b`). That commit says `GOOS=windows` and `GOOS=linux` `go build` and `go vet` passed and that nothing ran on those systems. This release preparation did not repeat those builds. Off macOS the vault fails with `ErrNoKeychain` before it runs `security`. There is no Windows or Linux app bundle.

### Breaking changes

- OpenRouter config keys were renamed (`cf5ef63cccd4186070b4950545e4048549faf896`). `jev_model`, `jev_key_env`, `daily_usd.jev`, `min_jev_prob`, and `decide.shadow` `jev` are now `openrouter_model`, `openrouter_key_env`, `daily_usd.openrouter`, `min_openrouter_prob`, and `openrouter`. Old configs and track files still load. A file that sets an old key and a new key to different values is refused. Save writes the new names. Ledger rows written earlier keep the name `jev`; `vani eval` and `vani label` map that name and say how many they mapped. The daily cap file stays `budget/jev.json` on purpose. README "Renamed keys (2026-10-19)" matches this commit and also records that `kbmatch.backend` `"jev"` became `"openrouter"` on 2026-10-20.

### Known issues

- Nothing publishes v0.1.0. There is no release workflow, no checksum, no SBOM, and no updater. Signing in `macos/scripts/bundle.sh` defaults to Apple Development with `--timestamp=none`, then `codesign --verify --deep --strict`. It does not notarize. The decision is GitPulse `ft-52b6e7d3f0950c24195d9c20c0a20351`.
- A clean clone cannot build. `go.work` is gitignored and required, `go.mod` replaces sibling checkouts, and `go.sum` is absent. The decision is GitPulse `ft-8d8ca2ea609144eb5240c2b743487d58`.
- `atomicfile.Write` replaces a symlinked path with a regular file. That bug is GitPulse `ft-22d7da5f2735997b8bbdb7934bc1345c`, still in progress, and this release does not change it.
- `CFBundleShortVersionString` and the extension manifest are `0.1.0`. `CFBundleVersion` is `1`. `extension/package.json` has no version field.
- Marketing version `0.1.0` is the extension manifest and the two short version strings above. There is no root `package.json`, no `Cargo.toml`, no `Cargo.lock`, and no `tauri.conf.json`.

## Commands to push and tag

Run these yourself, from `/Users/bharath/Code/apps/Vani`, after you re-count and accept the warnings. Do not add `--tags`. Do not force. Do not tag `aa8ed0c` on its own and leave the notes untagged. Tag the commit that contains `CHANGELOG.md` and `RELEASE_REPORT.md`.

```bash
git push origin main
git tag v0.1.0
git push origin v0.1.0
```

Before you run them:

- Origin has no `.github` workflows. Pushing the tag does not build a release, attach assets, or notarize anything.
- At `aa8ed0c`, `main` was 0 behind and 342 ahead of `origin/main` (`0dbee8a3ea8ecaff606b603b0a66e27f1de7bb80`), 421 commits in the repository. This documentation commit makes it one further ahead. Pushing `main` publishes that whole unpushed history.
- Vani is not in the GitPulse push order on `ft-aa3086e6`. That card says agents never touch tags. This session did not create `v0.1.0`.
- `bun test extension/test/` also runs other lanes' copies under `tmp/` whose paths contain that substring. The suite that passed is `bun test /Users/bharath/Code/apps/Vani/extension/test/`.
- If a pushed tag is wrong, do not move it. Ask before deleting `origin`'s `v0.1.0` or creating it again. Rollback text is still open on `ft-52b6e7d3f0950c24195d9c20c0a20351`.
