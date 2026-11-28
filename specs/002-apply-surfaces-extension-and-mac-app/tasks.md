# Tasks: 002 - Apply Surfaces: Browser Extension and Mac App

## Phase 1: Fill behaviour (Shipped)
- [x] [T-001] [US1] Fill with green and yellow confidence, leave unseen questions blank, and read every write back `extension/src/fill.ts` `extension/src/match.ts` tests: bun test --cwd /Users/bharath/Code/apps/Vani/extension test/fillwait.test.ts
- [x] [T-002] [P] [US1] Resolve one answer across every wording of a recognised question and refuse a label that reads as two `extension/src/intents.ts` tests: bun test --cwd /Users/bharath/Code/apps/Vani/extension test/intents.test.ts
- [x] [T-003] [P] [US1] Re-enter a fill after the page re-renders without double-writing `extension/src/frames.ts` tests: bun test --cwd /Users/bharath/Code/apps/Vani/extension test/fillreentry.test.ts
- [x] [T-004] [P] [US1] Audit a form for fields the fill missed `extension/src/formaudit.ts` tests: bun test --cwd /Users/bharath/Code/apps/Vani/extension test/formaudit.test.ts

## Phase 2: Bank and protocol (Shipped)
- [x] [T-005] [US2] Hold one revision-checked bank in the Mac app and return a conflict on a stale write `macos/Sources/VaniApplyKit/BankStore.swift` `macos/Sources/VaniApplyKit/SQLiteBankStore.swift` tests: swift test --package-path /Users/bharath/Code/apps/Vani/macos --filter BankStoreTests
- [x] [T-006] [P] [US2] Serve one protocol to the Chrome host and the Safari extension `macos/Sources/VaniApplyKit/NativeProtocol.swift` `macos/Sources/VaniApplyKit/NativeMessaging.swift` `macos/Sources/vani-apply-host/main.swift` tests: swift test --package-path /Users/bharath/Code/apps/Vani/macos --filter NativeMessagingTests
- [x] [T-007] [P] [US2] Speak the protocol from the extension background worker `extension/src/nativeport.ts` `extension/src/background.ts` tests: bun test --cwd /Users/bharath/Code/apps/Vani/extension test/nativeport.test.ts
- [x] [T-008] [P] [US2] Attack the protocol handler with oversized and malformed requests `macos/Tests/VaniApplyKitTests/NativeMessagingAttackTests.swift`

## Phase 3: Job ingest and Mac app clients (Shipped)
- [x] [T-009] [US5] Send scraped jobs to "vani jobs ingest" and let the ledger owner drain the spool `extension/src/ingest.ts` `extension/src/crawler.ts` `docs/EXTENSION_INGEST.md` tests: bun test --cwd /Users/bharath/Code/apps/Vani/extension test/ingest.test.ts
- [x] [T-010] [US4] Browse roles through "vani jobs ... --json", decoding leniently `macos/Sources/VaniApplyKit/Jobs.swift` `macos/Sources/VaniApplyKit/JobRow.swift` tests: swift test --package-path /Users/bharath/Code/apps/Vani/macos --filter JobsTests
- [x] [T-011] [P] [US4] Start a bounded manual run with a rotated log `macos/Sources/VaniApplyKit/ManualRun.swift` tests: swift test --package-path /Users/bharath/Code/apps/Vani/macos --filter ManualRunTests
- [x] [T-012] [P] [US4] Assemble the app bundle with its helpers `macos/scripts/bundle.sh` `macos/Package.swift`

## Phase 4: Gate runner (Shipped)
- [x] [T-013] [US1] Run the extension tests in parallel, re-run failing files serially, and say when a failure did not reproduce `scripts/test.sh` tests: go -C /Users/bharath/Code/apps/Vani test ./cmd/vani/ -run TestTestScriptBunPassFailAndFlake
- [x] [T-014] [P] [US1] Type-check the extension as its own suite `extension/scripts/typecheck.sh` `scripts/test.sh`
- [x] [T-015] [P] [US2] Run skip-capable Swift classes serially and count each skip `scripts/test.sh` tests: go -C /Users/bharath/Code/apps/Vani test ./cmd/vani/ -run TestTestScriptDerivesEverySkipCapableClass

## Phase 5: Open work
- [ ] [T-016] [US3] Decide and record whether the opt-in submit action in `extension/src/submit.ts` is intended, then make `extension/README.md` and the plan text agree with the code; the README says the extension never submits `extension/src/submit.ts` `extension/README.md` `extension/test/submit.test.ts` depends_on: [T-001]
- [ ] [T-017] [US2] Run the Safari sameQuestion path in Safari itself, which `docs/FOLLOWUPS.md` records as covered only by unit and in-process XPC tests `macos/Sources/VaniApplyExtension` `docs/FOLLOWUPS.md` depends_on: [T-006]
- [ ] [T-018] [US4] Compile and test the Swift files recorded as changed without a Swift toolchain on 2026-11-25 `macos/Sources/VaniApplyKit/Drafting.swift` `macos/Sources/VaniApplyKit/JobRow.swift` `docs/FOLLOWUPS.md`
