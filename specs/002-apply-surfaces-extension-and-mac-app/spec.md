# Feature: 002 - Apply Surfaces: Browser Extension and Mac App

**Status**: Active Specification | **Canonical Owner**: Vani `extension/` (TypeScript, built with Bun) and `macos/` (Swift package) | **Implementation State**: Shipped. Docs and code disagree on one point (US3); open items are in `tasks.md` Phase 5.

## 1. Problem Statement & Motivation

Finding a posting is only part of a job search; the other part is answering the same application questions on dozens of ATS forms. Vani Apply remembers the owner's answers and fills the next form in Chrome or Safari, and a Mac app holds the one answer bank, browses the roles the pipeline retrieved, and runs the pipeline on demand. The failure modes it is built against: writing a wrong or stale answer into a form without saying so, a field that silently keeps nothing, a copy of the bank in each browser that diverges, submitting an application without the owner's review, and a scraped or model-written value reaching a form unchecked.

This spec covers the browser extension (`extension/`), the native-messaging protocol between it and the Mac app, and the Mac app and helper binaries (`macos/`). The Go side of ingest is in `specs/001-listings-pipeline-and-decision-rules`. The owner's saved answers, settings and applied-job history are data and are not described here.

## 2. User Stories & Acceptance Scenarios

### US1: Fill marks what to check and never overwrites silently (Priority: P1)

As the owner, I want each filled field to say how confident the match was and to refuse to write what it cannot hold, so that I review the right fields.

**Acceptance Scenarios**:
- **Scenario 1: Confidence is visible**
  - **Given** a saved answer matching a question
  - **When** I press Fill
  - **Then** a confident match (same field on the same site, the page's `autocomplete` hint, or the same question) is outlined green, a fuzzy or long free-text match is outlined yellow, and unseen questions are left blank (`extension/README.md` "Use"; `extension/src/fill.ts`, `extension/src/match.ts`).
- **Scenario 2: Writes are read back and never truncated**
  - **Given** a saved answer longer than the field's `maxlength`
  - **When** Fill runs
  - **Then** nothing is written, the field is outlined yellow with a tooltip giving the answer's length and the field's limit, and a value a controlled field discards is retried once and then reported "manual", never "filled" (`extension/README.md`; `extension/test/fillwait.test.ts`, `extension/test/fillreentry.test.ts`).
- **Scenario 3: Stale answers fill yellow or not at all**
  - **Given** a start date, timeline, salary expectation or work location saved longer ago than its freshness window (21, 21, 90 and 180 days per the README)
  - **When** Fill runs
  - **Then** it fills yellow with the age in the tooltip, and a date already past is not filled (`extension/README.md`; `extension/src/intents.ts`, `extension/test/intents.test.ts`).
- **Scenario 4: Some answers are never offered**
  - **Given** other legal agreements, consents, and why-this-company essays
  - **When** Fill runs
  - **Then** it does not fill them; the arbitration answer stays empty until the owner chooses "I agree" (`extension/README.md`).

### US2: One answer bank, two browsers, no divergence (Priority: P1)

As the owner, I want Chrome and Safari to share one bank held by the Mac app, with conflicts detected rather than overwritten.

**Acceptance Scenarios**:
- **Scenario 1: Protocol with revision checks**
  - **Given** the native protocol in `macos/Sources/VaniApplyKit/NativeProtocol.swift` (`ping`, `get`, `put` with `baseRev`, `log`, `draft`, `contacts`, and the job and ingest operations)
  - **When** a `put` carries a `baseRev` older than the stored revision
  - **Then** the reply is `{"conflict":true,"rev":M,"bank":{...}}` and nothing is overwritten (`macos/Tests/VaniApplyKitTests/NativeMessagingTests.swift`, `NativeMessagingAttackTests.swift`, `BankStoreTests.swift`).
- **Scenario 2: Chrome and Safari speak the same protocol**
  - **Given** the Chrome native host (length-prefixed JSON on stdio, `macos/Sources/vani-apply-host/main.swift`) and the Safari native extension
  - **When** either sends a request
  - **Then** both are served by the same handler in `NativeMessaging.swift` (header comment of `NativeProtocol.swift`).
- **Scenario 3: A failed contacts lookup is an error, not an empty list**
  - **Given** the contacts bank executable is missing
  - **When** `contacts list` is requested
  - **Then** the reply is an error with a code, never `{"keys":[]}`, and a check that did not run reports `status: "not_run"` (`NativeProtocol.swift` header).

### US3: Applying stays under the owner's hand (Priority: P1)

As the owner, I want Fill to write answers and stop, and any click on a final-submit control to be a separate, explicit act.

**Acceptance Scenarios**:
- **Scenario 1: Fill does not submit**
  - **Given** the Fill action
  - **When** it completes
  - **Then** the Submit button and file uploads are untouched, apart from the resume attachment the owner chose for that job, which is yellow and needs a check (`extension/README.md` "Never touched"; `extension/src/fill.ts`).
- **Scenario 2: Submit is a separate, off-by-default action**
  - **Given** `extension/src/submit.ts`
  - **When** the popup sends `submitPage` with `armed: true`
  - **Then** the code clicks at most one control that `isFinalSubmit` matches and types no password, captcha or agreement; it does nothing when `armed` is false or when the audit did not run (`extension/src/submit.ts` header; `extension/test/submit.test.ts`).
- **Scenario 3: Documents and code disagree**
  - **Given** the README and plan text say "It never submits"
  - **When** read against `extension/src/submit.ts` and `popup.ts` (`type: "submitPage"`)
  - **Then** the statement is true of Fill and not of the opt-in submit action, and the discrepancy is recorded as an open task for the owner rather than resolved here (T-016).

### US4: The Mac app is a client of the CLI, not a second implementation (Priority: P2)

As the owner, I want the app's Jobs and Pipeline tabs to show exactly what `vani` reports.

**Acceptance Scenarios**:
- **Scenario 1: Jobs reads the ledger through `vani jobs ... --json`**
  - **Given** the helper binary `Contents/Helpers/vani` and the configured config and state folders
  - **When** the Jobs tab loads
  - **Then** it decodes `vani jobs ... --json` leniently, shows nothing it could not decode as a value ("not known", never 0), and works while a run holds the ledger because it reads without the run's lock (`macos/README.md`; `macos/Sources/VaniApplyKit/Jobs.swift`; `macos/Tests/VaniApplyKitTests/JobsTests.swift`, `JobsUnknownEnumTests.swift`).
- **Scenario 2: Run Now starts a bounded, logged run**
  - **Given** the Pipeline tab's Run Now sheet
  - **When** the owner confirms (Start Run is what spends)
  - **Then** the app starts `vani run-once` with the chosen per-run options and `VANI_RUN_LOG`, rotates the previous `manual.log` into `<state>/runs/history/`, and passes the decision key through the environment; with `VANI_NO_KEYCHAIN=1` set in every `vani` it starts (`macos/README.md`; `macos/Sources/VaniApplyKit/ManualRun.swift`; `ManualRunTests.swift`).

### US5: Scraped job lists reach the ledger through the spool (Priority: P2)

As the owner, I want a job list I scrape in the browser to become postings Vani triages, without the extension writing the ledger.

**Acceptance Scenarios**:
- **Scenario 1: End to end flow**
  - **Given** a listing page the extension scraped
  - **When** the owner sends the jobs to Vani
  - **Then** `background.ts` sends the native op `jobsIngest`, the host runs `vani jobs ingest --json` with the request on stdin, and the Go side validates and spools it (`docs/EXTENSION_INGEST.md`; `extension/src/ingest.ts`, `extension/src/crawler.ts`; `extension/test/ingest.test.ts`).

## 3. Success Metrics & Testability Criteria

- **SC-001**: `scripts/test.sh --suite bun` runs `bun test --parallel` over `extension/test` and, if a file fails in parallel, a serial re-run; a failure that vanishes serially is reported as such and fails under `--strict` (`run_bun` in `scripts/test.sh`).
- **SC-002**: `scripts/test.sh --suite types` runs `extension/scripts/typecheck.sh`, because `bun test` does not type-check.
- **SC-003**: `scripts/test.sh --suite swift` counts every skipped or not-run Swift test in its summary, and the window-driving UI tests are NOT RUN unless `--ui` is given.
- **SC-004**: The Chrome extension id is fixed by the public key in `extension/static/chrome-key.json` and is the id the Mac app's native-messaging manifest allows (`extension/README.md`).
- **SC-005**: The resume file attached on Fill is at most 512 KB (`extension/README.md`).
- **SC-006**: The Swift suite and the Bun suite are not run by hosted CI (`docs/TESTING.md`); they run only on the owner's Mac.

## 4. Edge Cases & Exclusions

- **Exclusion**: the on-device field triage (`vani-fm`, Apple Foundation Models), the schedule agent, and the Resume Studio resume builder are part of the app but are described only by their README sections; they are not specified acceptance-by-acceptance here.
- **Exclusion**: no answer, saved value or applied-job record is stated in this spec; they are the owner's data.
- **Edge**: personal data never enters a model prompt, log line or fixture (`CLAUDE.md`); the resume's file name is never written to the activity log (`extension/README.md`).
- **Edge**: Safari's `sameQuestion` path goes through the agent over XPC and has unit and in-process XPC tests but was not run in Safari itself (`docs/FOLLOWUPS.md`, Autofill rework 2026-10-28).
- **Edge**: the extension's `linkedin.ts` and `jobsites.ts` search launchers: LinkedIn support is a launcher for search pages; this spec makes no claim about it because the file was being edited by another session when it was written.
