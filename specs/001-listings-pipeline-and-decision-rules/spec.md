# Feature: 001 - Listings Pipeline and Decision Rules

**Status**: Active Specification | **Canonical Owner**: Vani (`github.com/bharathvbcr/Vani`), root module and nested `decide/` | **Implementation State**: Shipped. Open items are recorded in `docs/FOLLOWUPS.md` and listed in `tasks.md` Phase 6.

## 1. Problem Statement & Motivation

Vani is a personal, listings-first job search engine. It finds postings on company ATS boards and job boards, removes what the owner does not want without spending model tokens, lets a decision model read the rest in a way that can only annotate, ranks the survivors, and writes a daily digest. The failure modes it is built against are: an unbounded or unsafe network call, a model that silently drops a good posting, a half-written ledger after a crash, two writers corrupting the ledger, and a check that could not run being read as a pass.

This specification describes the Go pipeline as shipped: sources, egress, the append-only ledger, the stages a posting passes, the decision rules, the scheduler and the extension-ingest spool. The apply surfaces (browser extension and Mac app) are specified in `specs/002-apply-surfaces-extension-and-mac-app`; the knowledge-base tool `rkb` in `specs/003-rkb-knowledge-base-tool`. The owner's live configuration, state and spend are not described here.

## 2. User Stories & Acceptance Scenarios

### US1: Every network request is allowlisted and bounded (Priority: P1)

As the owner, I want no request to leave the process except through one audited path, so that an ATS response, a redirect or a job description cannot reach a host I did not name.

**Acceptance Scenarios**:
- **Scenario 1: https only, allowlisted hosts, no private addresses**
  - **Given** a request through `egress.Doer`
  - **When** the scheme is not https, the host is not in `allowed_hosts`, or the address resolves to a loopback, private or link-local range (including through DNS64 or NAT64)
  - **Then** the request is refused with a typed error such as `ErrScheme`, on the initial request and on every redirect hop (`internal/egress/client.go`, `internal/egress/doer.go`; `internal/egress/nat64_test.go`, `internal/egress/hardening_test.go`, `internal/egress/loopback_test.go`).
- **Scenario 2: No direct HTTP elsewhere**
  - **Given** the repository rule "No `http.DefaultClient`, no `http.Get`" in `CLAUDE.md`
  - **When** a source adapter lists a board
  - **Then** it does so through a `Doer` (`internal/sources/*`; the adapters share `internal/sources/srcutil`).

### US2: The ledger is append-only, single-writer and crash-safe (Priority: P1)

As the owner, I want every posting and every state change recorded durably, so that a crash or a second process cannot lose or corrupt history.

**Acceptance Scenarios**:
- **Scenario 1: Events are immutable and folded to records**
  - **Given** `ledger.Event` lines (`v`, `at`, `key`, `status`, optional `posting`, `annotations`, `reason`)
  - **When** the ledger is opened
  - **Then** the canonical store is append-only JSONL, any index is derived and rebuilt on start, and `Record` is the folded current state (`internal/ledger/types.go`, `internal/ledger/file.go`, `internal/ledger/reader.go`; `internal/ledger/durability_test.go`, `internal/ledger/dirsync_test.go`).
- **Scenario 2: One writer at a time**
  - **Given** a running daemon that holds the ledger's exclusive lock
  - **When** `vani jobs ingest` receives scraped jobs
  - **Then** it validates them and writes a batch file to an inbox spool instead of writing the ledger, and the lock holder drains the spool every two minutes and at the start of each daily run (`internal/inbox/inbox.go`, `docs/EXTENSION_INGEST.md`).
- **Scenario 3: A stuck ledger ends the daemon rather than failing silently**
  - **Given** an inbox poll that leaves the ledger unusable
  - **When** the daemon job returns `daemon.ErrFatal`
  - **Then** the daemon exits non-zero within one poll interval (`internal/daemon/daemon.go`, `docs/EXTENSION_INGEST.md`).

### US3: Decision models annotate and never drop (Rule 7) (Priority: P1)

As the owner, I want no model answer to remove a posting or widen access by itself, so that an uncertain or wrong reading cannot silently lose a role.

**Acceptance Scenarios**:
- **Scenario 1: Gated packages cannot import the decision layer**
  - **Given** the packages `vault`, `notify` and `approve`
  - **When** `TestImportBoundary` parses imports under `internal/`
  - **Then** it fails if any of them imports the shared `decide` module or `internal/decide/*`, and `TestImportBoundaryDetectsViolations` proves the check detects a planted violation (`internal/rule7/boundary_test.go`; the `decide` module also has its own `decide/boundary_test.go`).
- **Scenario 2: An answer is always one of the caller's options**
  - **Given** a `decide.Question` with options
  - **When** a backend replies
  - **Then** the reading is answered, abstained, refused, failed, unavailable or not asked, an answered value is one of the options, and nothing in the module means "allow" (`decide/README.md`, `decide/types.go`, `decide/validate.go`; `decide/formal_invariants_test.go`).
- **Scenario 3: An uncertain reading keeps the posting**
  - **Given** a triage that cannot decide
  - **When** the posting is folded
  - **Then** its status is `unsure`, it is kept and flagged in the digest, and dropping is an engine rule keyed on an annotation (`internal/posting/posting.go` statuses; `internal/pipeline/dropcandidates.go`, `internal/pipeline/failopen.go`; `CLAUDE.md` rules).
- **Scenario 4: The triage training contract is pinned**
  - **Given** `internal/decide/jdtriage/fixture.json`
  - **When** `jdtriage_test.go` runs
  - **Then** its sha256 must equal the pinned `fixtureSHA256`; changing the fixture means a retrain (`CLAUDE.md`).

### US4: Spend is bounded per day (Priority: P2)

As the owner, I want a hard daily cap on decision-model spend that a retry or a mis-priced reply cannot bypass.

**Acceptance Scenarios**:
- **Scenario 1: Cap enforced before the reply is trusted**
  - **Given** a `decide.Spend` with a `DailyCap`
  - **When** the OpenRouter client (`decide/jev`) makes a call
  - **Then** spend is charged before the reply is trusted, a reply that reports tokens but no cost is priced from `Config.InputUSDPerMTok`, and a reported cost wins (`decide/spend.go`, `decide/spend_test.go`; `decide/README.md`).

### US5: A daily run is deterministic first and bounded throughout (Priority: P1)

As the owner, I want the cheap deterministic stages to remove what I excluded before any model sees it, and every run to be bounded.

**Acceptance Scenarios**:
- **Scenario 1: Stages in order, each recorded**
  - **Given** a watchlist of boards
  - **When** `vani run-once` runs
  - **Then** postings move through discovered, live, triaged or unsure, scored, explained and digested, or end dead, duplicate or dropped, each transition appended to the ledger with a reason (`internal/pipeline/run.go`, `internal/pipeline/reconcile.go`, `internal/pipeline/advance.go`, `internal/pipeline/triagestep.go`; statuses in `internal/posting/posting.go`).
- **Scenario 2: Config exclusions are applied without a model**
  - **Given** exclusion rules (titles, companies, locations, descriptions) in the config
  - **When** a run folds postings
  - **Then** matching postings are dropped by an engine rule and reopened when a rule no longer matches (`internal/config/exclude.go`, `internal/pipeline/exclude.go`; `vani prefilter` shows the effect first, per `cmd/vani/main.go` usage).
- **Scenario 3: A removed board closes its postings; a missed run is bounded**
  - **Given** a board removed from the watchlist, or a machine asleep through its slot
  - **When** the next run starts
  - **Then** the board's open postings are closed unless `--keep-postings`, and a scheduled run late by more than `MissedRunWindow` (6 hours) is skipped rather than run (`internal/pipeline/removedboard.go`, `internal/daemon/daemon.go`; `internal/daemon/daemon_test.go`).

## 3. Success Metrics & Testability Criteria

- **SC-001**: `go -C /Users/bharath/Code/apps/Vani test ./internal/rule7/ ./internal/egress/ ./internal/ledger/` exits 0 (the import boundary, egress and ledger durability suites).
- **SC-002**: `GOWORK=off go -C /Users/bharath/Code/apps/Vani/decide test ./...` exits 0; the shared module is stdlib-only, enforced by `decide/boundary_test.go`.
- **SC-003**: `internal/decide/jdtriage/fixture.json` hashes to the `fixtureSHA256` constant in `internal/decide/jdtriage/jdtriage_test.go`.
- **SC-004**: `daemon.MissedRunWindow` is 6 hours and `wakeSlice` is 30 seconds in `internal/daemon/daemon.go`.
- **SC-005**: `scripts/test.sh --suite go` reports a failure if any listed `cmd/vani` test was not reported by a shard, so an unrun test cannot read as a pass (`run_go` in `scripts/test.sh`).
- **SC-006**: `scripts/test.sh --suite specs` exits 0 only when `devcouncil spec validate --all --strict` exits 0 and exits non-zero when the `devcouncil` binary is missing.

## 4. Edge Cases & Exclusions

- **Exclusion**: no personal data (name, contact, work authorization) enters a model prompt, log line, ledger annotation or test fixture (`CLAUDE.md`); job description text is untrusted and framed as such in prompts.
- **Exclusion**: Vani never applies to a job on its own; applying is human-in-the-loop and belongs to spec 002.
- **Edge**: a windowed board (ZipRecruiter, Dice, Adzuna) holds absent postings rather than closing them (`pipeline.ReconcileWindow`, `docs/FOLLOWUPS.md`).
- **Edge**: the Mac app may start its own `vani run-once`; `run-once` is refused while the daemon holds the ledger, and `run-request` asks the running daemon to make the run (`cmd/vani/main.go` usage).
- **Edge**: the Windows build is vetted but `rekey` refuses on Windows (README platform table; `docs/FOLLOWUPS.md` item A2-7).
