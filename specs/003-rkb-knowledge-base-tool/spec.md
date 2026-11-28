# Feature: 003 - rkb Knowledge-Base Tool and Eligibility Gate

**Status**: Active Specification | **Canonical Owner**: Vani nested module `rkb/` (stdlib-only Go, `GOWORK=off`) and its Vani-side consumers `internal/kbsync`, `internal/rkbexec`, `internal/rkbresume` | **Implementation State**: Shipped. Open items in `tasks.md` Phase 5.

## 1. Problem Statement & Motivation

A resume or an application answer that states a figure the evidence does not support is worse than none. `rkb` keeps one evidence-backed bullet bank (`bank.json`), small selections of it (`resumes/*.json`), and the figures behind every bullet (`Claims.tex`), each checked against the artifact it came from. It lints bullets, runs blind verification passes, renders HTML, PDF, DOCX, text and LaTeX, and exports the approved claims. Vani never builds the bank; it runs the `rkb` binary and reads only the bullets that pass one eligibility rule.

The failure modes it is built against: a half-written bank after a crash or a concurrent writer, a bullet citing a figure the claims file has withdrawn, a wording no second reader has passed reaching an employer, a release that skipped a check, a child process outliving its timeout, and owner-specific vocabulary compiled into a shared tool.

This spec describes the tool, its command surface, its fail-closed release path, and Vani's eligibility gate. It does not restate any bullet, figure, claim or project; those are the owner's career data and live outside this repository. It also does not claim to describe the resume pipeline in `~/Code/career`, which is a separate repository that uses an `rkb` binary.

## 2. User Stories & Acceptance Scenarios

### US1: A shared tool carries no owner data (Priority: P1)

As a maintainer, I want the tool to hold generic defaults and take the owner's vocabulary from configuration, so the module stays publishable and its fixtures stay synthetic.

**Acceptance Scenarios**:
- **Scenario 1: Config layering**
  - **Given** the generic default embedded from `rkb/assets/config.json`
  - **When** a user file exists at `<skill>/rkb/assets/config.json` or `~/.config/rkb/config.json`
  - **Then** keys it sets replace the default's, keys it omits keep the default, and a user file that exists but cannot be read or parsed stops `rkb` rather than falling back (`rkb/README.md` "Configuration"; `rkb/config_test.go`, `rkb/personal_rules_test.go`).
- **Scenario 2: Synthetic fixtures only**
  - **Given** `rkb/examples/` and `rkb/testdata/`
  - **When** the module is built and tested
  - **Then** no owner vocabulary is compiled in; owner words live in the config's `bullet_rules` and `repos` keys (`CLAUDE.md`; `rkb/README.md`).
- **Scenario 3: Stdlib only**
  - **Given** `rkb/go.mod`
  - **When** the module is built with `GOWORK=off`
  - **Then** it needs no third-party module (`CLAUDE.md`: "stdlib only").

### US2: The bank cannot be corrupted by a crash or a second writer (Priority: P1)

As the owner, I want every file `rkb` rewrites to be replaced atomically and guarded against concurrent writers.

**Acceptance Scenarios**:
- **Scenario 1: Atomic replace**
  - **Given** a write of `bank.json` or `Claims.verify.json`
  - **When** the write runs
  - **Then** bytes go to a sibling temp file, are fsynced, and replace the target with one rename, so a crash or full disk leaves the old file or the new one (`rkb/safeio.go` header; `rkb/w3a_atomic_test.go`).
- **Scenario 2: Advisory lock and compare-and-swap**
  - **Given** two `rkb` processes
  - **When** both edit shared state
  - **Then** an advisory lock on `<file>.lock` serialises them, the lock file is never deleted, and a write based on stale bytes is refused by comparing the sha256 read earlier (`rkb/safeio.go`; `rkb/lock_unix.go`, `rkb/lock_other.go`).
- **Scenario 3: A crashed validator run never replaces a good report**
  - **Given** `rkb claims vm`
  - **When** the run crashes
  - **Then** the previous report is kept (`rkb/main.go` usage text; `rkb/w3a_verify_test.go`).

### US3: Release is a single fail-closed command (Priority: P1)

As the owner, I want one command that either publishes a verified resume or refuses with the check that failed, and a manifest of what was verified.

**Acceptance Scenarios**:
- **Scenario 1: Ordered checks, any failure stops**
  - **Given** `rkb finalize <resume> --claims claims.json`
  - **When** it runs
  - **Then** it checks in order that claims are for this exact resume and fresh, that the ecosystem report is fresh and every declared link verified or partial, `lint --final`, `render --final`, `fit --strict --final`, the ATS text layer, the Word export within the page count, and LaTeX; output is staged and then published, and `manifest.json` records every check and the hash of every input and output (`rkb/finalize.go` header comment; `rkb/w3a_stage_test.go`).
- **Scenario 2: Drafts are not releases**
  - **Given** `rkb build`
  - **When** it runs
  - **Then** it writes drafts with unverified figures highlighted and gates nothing (`rkb/finalize.go` comment on `build`).
- **Scenario 3: A tool that is not installed is a refusal, not a pass**
  - **Given** the PDF, text-layer or Word steps need Chrome, poppler or LibreOffice
  - **When** one is missing
  - **Then** `rkb doctor` reports it, and the dependent step fails rather than being skipped (`rkb/doctor.go`, `rkb/README.md`).

### US4: Only bullets two readers passed can leave the pipeline (Priority: P1)

As the owner, I want Vani to read only the bullets that pass one eligibility rule, so that a superseded wording or an unreviewed draft cannot reach an employer.

**Acceptance Scenarios**:
- **Scenario 1: One rule for every outlet**
  - **Given** the bank, the owner's current selections and a claims export
  - **When** `kb sync`, `draft`, `suggest`, `kbmatch`, `resume` or the semantic profile needs bullets
  - **Then** each uses `kbsync.Eligible`: a current selection lists the bullet, every claim token resolves, no token flagged CAUTION is used unless the bullet names it, and for an HR reader and a hiring-manager reader each the latest round-2 verdict on this exact wording is a pass with a second pass behind it (`internal/kbsync/eligible.go`; `internal/kbsync/eligible_test.go`, `eligible_redteam_test.go`).
- **Scenario 2: The rule is held to rkb by an oracle**
  - **Given** Vani mirrors rkb's review hash and selection defaults
  - **When** the real-bank oracle test runs
  - **Then** it fails if the mirrored rule and rkb disagree (`internal/kbsync/realbank_test.go`; the test may skip when the bank is absent, which the runner does not count, see `research.md`).
- **Scenario 3: `rkb` runs bounded**
  - **Given** any `rkb` invocation from Vani
  - **When** it exceeds its time or output bound, or is cancelled
  - **Then** `rkbexec.ExecRunner` stops the whole process tree and returns an error (`internal/rkbexec/runner.go`, `internal/rkbexec/procgroup_unix.go`; `procgroup_hostile_test.go`).

### US5: Bullets are linted for words as well as figures (Priority: P2)

As the owner, I want repeated, jargon-heavy or unsupported phrasing refused before a reader sees it.

**Acceptance Scenarios**:
- **Scenario 1: Anti-vanity and jargon gates are configurable**
  - **Given** `bullet_rules` in the config (`codenames`, `lead_jargon`, `tech_terms`, `extra_banned`, `dup_phrases`, `summary_banned`, `mechanisms`)
  - **When** `rkb lint` runs
  - **Then** matches become errors or warnings, and a malformed `mechanisms` entry stops `rkb` (`rkb/lint.go`; `rkb/README.md`; `rkb/guards_test.go`, `rkb/harden_test.go`).

## 3. Success Metrics & Testability Criteria

- **SC-001**: `GOWORK=off go -C /Users/bharath/Code/apps/Vani/rkb test ./...` exits 0. It did so once on this working tree while writing this spec, in 38 seconds, with other sessions' uncommitted edits present (`rkb/quote.go`, `rkb/w2a_numbers_test.go`); it was not run on `HEAD`.
- **SC-002**: `scripts/test.sh --suite go` tests the nested `rkb` module on its own with `GOWORK=off`, fails if its `go.mod` is missing, and vets it for `windows/amd64` (`run_go`, `GO_NESTED` in `scripts/test.sh`).
- **SC-003**: Hosted CI runs the nested modules through `scripts/test.sh --suite go --fresh` on Linux and macOS (`docs/TESTING.md`; `.github/workflows/fresh-install.yml`).
- **SC-004**: `rkb finalize` has eight ordered checks (comment in `rkb/finalize.go`) and writes a manifest of input and output hashes.
- **SC-005**: Eligibility needs a pass from each of two reader roles on the exact current wording (`text_hash`, the first 12 hex characters of a sha1 over the resolved plain text); the round-2 reviewer is named, is not `drafter` and is not the bullet's author (`internal/kbsync/eligible.go`).

## 4. Edge Cases & Exclusions

- **Exclusion**: no bullet text, figure, claim id, project name or reviewer verdict is stated in this spec; they are career data.
- **Exclusion**: the career repository's own resume pipeline, templates and published documents are outside Vani and outside this spec.
- **Edge**: the rule in `kbsync.Eligible` is a subset of rkb's final gate. It does not re-run figure blind passes, statement checks, say-it-once rules or page fit; a resume Vani builds still passes rkb `lint` and `fit` (`internal/kbsync/eligible.go` comment).
- **Edge**: external tools are needed only by the commands that use them: Chrome or Chromium (PDF, page fill), poppler `pdftotext` and `pdfinfo`, optionally LibreOffice and `pdflatex` (`rkb/README.md`).
- **Edge**: `rkb doctor` is the check for config, repos, `Claims.tex`, bank, log, tools and binary; it was not run for this spec.
