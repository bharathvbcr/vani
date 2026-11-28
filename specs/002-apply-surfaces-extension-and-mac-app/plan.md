# Implementation Plan: 002 - Apply Surfaces: Browser Extension and Mac App

## 1. Technical Context

- **Languages**: TypeScript for the extension (`extension/src/*.ts`, typechecked by `extension/scripts/typecheck.sh` with `typescript` pinned in `extension/package.json`; tests run with `bun test`), Swift for the Mac app and helpers (`macos/Package.swift`, SwiftPM, no Xcode project). Each uses the platform's own language; no cross-platform UI framework.
- **Extension build**: `bun run extension/build.ts` produces `extension/dist-chrome` (Chrome, fixed id from `extension/static/chrome-key.json`) and `extension/dist-safari` (same code with a Safari-style manifest). `extension/static/manifest.json` and `popup.html` are the static parts.
- **Swift targets** (directories under `macos/Sources/`): `VaniApplyKit` (library: bank, protocol, jobs, run control), `VaniApply` (the app), `VaniApplyExtension` (Safari web extension), `vani-apply-host` (Chrome native host), `vani-apply-agent`, `vani-schedule-agent`, `vani-fm` (on-device field triage). `macos/scripts/bundle.sh` assembles the bundle.
- **Protocol**: native messaging, one request and one reply per message, documented in the header comment of `macos/Sources/VaniApplyKit/NativeProtocol.swift` and handled by `NativeMessaging.swift`; the extension's side is `extension/src/nativeport.ts` and `background.ts`.
- **Persistence**: the answer bank in the app's App Group container behind `BankStore.swift` and `SQLiteBankStore.swift`; a revision number guards writes. Per-job resume PDFs are kept in the same container. The extension holds no durable copy that can diverge (the README describes a local-to-app migration merged at most once).
- **Boundary to Go**: the app runs the bundled `vani` helper (`vani jobs ... --json`, `vani status --json`, `vani run-once`, `vani jobs ingest --json`); contracts live in `cmd/vani` and `docs/EXTENSION_INGEST.md`.
- **Gate**: `scripts/test.sh` suites `bun`, `types` and `swift`, plus `specs`. None of the three runs in hosted CI.

## 2. Constitution Check & Complexity Tracking

Judged against the nine articles of DevCouncil's `constitution.md`, which is DevCouncil-specific; no repo-local constitution exists for Vani.

| Article | Status | Compliance Details / Justification |
| :--- | :--- | :--- |
| **I: Library-First** | PASSED | `VaniApplyKit` holds the logic; the app, host and agents are thin executables over it. The extension separates `src/*.ts` modules from `content.ts` and `popup.ts`. |
| **II: CLI as First-Class Consumer** | PASSED | The app drives `vani ... --json`; it adds no pipeline logic of its own (`macos/README.md`). |
| **III: Test-First** | PASSED | Test directories `extension/test/` and `macos/Tests/` carry unit, adversarial and attack suites; the runner counts skips. Not re-run here. |
| **IV: Fail-Closed Gates** | PASSED | A write a field did not keep is "manual", a contacts lookup that could not run is `not_run` or an error, a typecheck is a separate suite because `bun test` does not check types, and Swift skips are counted in the summary. |
| **V: Single Source of Truth** | PASSED | One bank in the app; one native protocol shared by Chrome and Safari; the Jobs tab decodes the CLI rather than re-deriving ranking. |
| **VI: Memory & Allocator Symmetry** | N/A | No manual memory management in these targets. |
| **VII: Simplicity Limit (<= 3 modules)** | PARTIAL | The Swift package has several targets (a library, an app, an extension, three helpers); each is an executable boundary required by macOS, not an abstraction layer. |
| **VIII: Anti-Abstraction** | PASSED | The protocol is plain JSON ops; no RPC framework. |
| **IX: Observability & Auditability** | PASSED | The native `log` op records fill events with counts only; `<state>/suggest/outcomes.jsonl` records draft outcomes; run logs are rotated into `<state>/runs/history/`. |

## 3. Complexity Tracking

- `extension/src/popup.ts` and `extension/src/content.ts` are very large single files. Splitting them is not proposed here; they are listed so that a change to either is checked against the existing tests first.
- The documentation says "never submits" while `extension/src/submit.ts` implements an opt-in submit action; reconciling the two is an owner decision (T-016), not a code change made in this spec.
