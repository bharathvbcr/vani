# Technical Research & Alternatives

## 1. Options Evaluated

- **One bank in the app versus one per browser.** The README states the Mac app holds the bank for both Chrome and Safari; the extension speaks to it through a revision-checked protocol, so two browsers cannot silently diverge (`macos/README.md`, `NativeProtocol.swift`).
- **Extension never writes the ledger.** The ledger has one writer, so scraped jobs travel extension, native host, `vani jobs ingest`, an inbox spool, then the ledger owner (`docs/EXTENSION_INGEST.md`).
- **App as CLI client.** The Jobs and Pipeline tabs decode `vani ... --json` instead of reimplementing ranking, which keeps the CLI and the app in step (`macos/README.md`).
- **Separate suites for tests, types and Swift.** `bun test` does not type-check, and `swift test` prints a skipped test like a passed one, so the runner adds a typecheck suite and a serial Swift pass that counts skips (comments in `scripts/test.sh`).

## 2. Evidence Gathered While Writing This Spec

- Read in this session: the header of `macos/Sources/VaniApplyKit/NativeProtocol.swift`, the header and `armed` handling of `extension/src/submit.ts`, `extension/README.md` sections "Use", "The resume for each job" and "Never touched", `macos/README.md` Jobs and Pipeline text, `docs/EXTENSION_INGEST.md`, and the scripts section for `bun`, `types` and `swift` in `scripts/test.sh`.
- **A documentation conflict was found**: `extension/README.md` line 9 and a flow diagram say the extension never submits, while `extension/src/submit.ts` implements a "Submit this page" action, reached from `extension/src/popup.ts` (`{ type: "submitPage", armed: true }`) and `content.ts`, described in its own header as off until the owner turns it on and clicking at most one final-submit control. Per the rule that code wins over documents, the spec states the code's behaviour and records the discrepancy as T-016. Whether the armed control is reachable without a deliberate owner action in the shipped popup was not traced.
- Every path cited in `tasks.md` was checked to exist with `test -e`.

## 3. Unverified

- **Nothing was executed**: no `bun test`, typecheck, `swift build` or `swift test`. Their results are not claimed. The working tree held other sessions' uncommitted edits to `extension/src/content.ts`, `fill.ts`, `popup.ts`, `scraper.ts`, `crawler.ts`, `frames.ts`, `jobinfo.ts`, `jobsites.ts`, `messages.ts`, `relevance.ts`, `detail.ts`, `crawlrun.ts`, `extension/static/popup.html` and several `extension/test` files, so the files cited here may differ from `HEAD`. `extension/src/linkedin.ts` and its test are untracked work from another session and are not cited.
- **DevMap was degraded** (`analyzer_freshness: false`) when this was written; no graph query was used. Claims rest on reading source.
- The full list of native operations was not read: only the header comment's `ping`, `get`, `put`, `log`, `draft` and `contacts` were seen directly. `jobsIngest` is from `docs/EXTENSION_INGEST.md`. The wording "job operations" in the spec is from the existence of `macos/Sources/VaniApplyKit/JobIngest.swift` and `Jobs.swift`, not from reading them.
- The freshness windows (21, 21, 90, 180 days), the 512 KB resume cap and the "Never touched" list are from the README, not from code; they were not matched to `extension/src/intents.ts` or `resume.ts`.
- Safari behaviour was not checked; `docs/FOLLOWUPS.md` says Safari's `sameQuestion` path was not run in Safari itself.
- Whether the Chrome extension id in `extension/static/chrome-key.json` matches the id the Mac app's native-messaging manifest allows was taken from the README, not compared.
- The swift sources `Drafting.swift`, `ScheduleAgent.swift`, `Watchlist.swift`, `JobRow.swift` and `JobsView.swift` were recorded in `docs/FOLLOWUPS.md` (2026-11-25) as changed without a Swift toolchain in the build container; whether they now compile was not checked.
