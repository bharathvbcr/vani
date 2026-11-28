# Implementation Plan: 003 - rkb Knowledge-Base Tool and Eligibility Gate

## 1. Technical Context

- **Language**: Go only. `rkb/` is a nested module (`module github.com/bharathvbcr/Vani/rkb`, `go 1.27.4` in `rkb/go.mod`), standard library only, built and tested with `GOWORK=off` because the repository's `go.work` does not include it (`rkb/README.md` "Build"; `CLAUDE.md`).
- **Shape**: one `package main` binary. Commands are dispatched in `rkb/main.go`, which also holds the usage text, the per-command flag table and the positional-argument table; unknown or repeated flags are refused.
- **Files by area**: bank and selections (`bank.go`, `bankcheck.go`, `importtex.go`), claims and verification (`claims.go`, `quote.go`, `numbers.go`, `kbcheck.go`, `eco.go`, `assess.go`), words (`lint.go`, `guards.go`, `repeats.go`), outputs (`render.go`, `fit.go`, `ats.go`, `docx.go`, `latex.go`, `texconv.go`), release (`finalize.go`), environment (`doctor.go`, `setup.go`), safety (`safeio.go`, `lock_unix.go`, `lock_other.go`, `proc.go`, `proc_unix.go`, `proc_other.go`, `signals.go`).
- **JSON**: `encoding/json/jsontext` (JSON v2). The bytes rkb writes are pinned by golden files in `rkb/testdata/json-contract/`, written by the earlier v1 build (`rkb/json_contract_test.go`).
- **Configuration**: the generic default embedded from `rkb/assets/config.json`, layered with one user file (`rkb/README.md` "Configuration").
- **External tools**: Chrome or Chromium, poppler `pdftotext` and `pdfinfo`, optionally LibreOffice and `pdflatex`; each needed only by the commands that use it.
- **Vani-side consumers**: `internal/kbsync` (the eligibility rule and DevPrism sync), `internal/rkbexec` (bounded process-tree runner), `internal/rkbresume` (per-job resume builds through rkb).
- **Persistence**: the owner's `bank.json`, `resumes/*.json`, `Claims.tex` and `Claims.verify.json` live in the career repository, outside Vani; rkb writes them atomically under a lock. Nothing of them is stored in this repository.
- **Gate**: `scripts/test.sh --suite go` tests `rkb` on its own (`GO_NESTED` in `scripts/test.sh`); hosted CI runs that suite through `.github/workflows/fresh-install.yml`. The spec gate is the `specs` suite of `scripts/test.sh`.

## 2. Constitution Check & Complexity Tracking

Judged against the nine articles of DevCouncil's `constitution.md`, which is DevCouncil-specific; Vani's own rules are in `CLAUDE.md` and no repo-local constitution was created (see `specs/001-listings-pipeline-and-decision-rules/plan.md`).

| Article | Status | Compliance Details / Justification |
| :--- | :--- | :--- |
| **I: Library-First** | PARTIAL | rkb is a single `package main`; its logic is not importable. The Vani side is library-first (`internal/kbsync`, `internal/rkbexec`, `internal/rkbresume`) and reaches rkb only as a binary. |
| **II: CLI as First-Class Consumer** | PASSED | Every capability is an `rkb` subcommand with a fixed flag table; `bank check` and `eco` write JSON reports (`--json`); `doctor` checks the setup. |
| **III: Test-First** | PASSED | `CLAUDE.md` requires adversarial tests for every behaviour; rkb carries hardening, atomic-write, signal, process and golden suites (`rkb/harden_test.go`, `rkb/w3a_atomic_test.go`, `rkb/w3a_signal_test.go`, `rkb/w3a_proc_test.go`, `rkb/assess_golden_test.go`). Order of writing not re-verified. |
| **IV: Fail-Closed Gates** | PASSED | `finalize` stops at the first failing check; a missing external tool fails the step that needs it; an unreadable user config stops rkb; a stale-bytes write is refused (`rkb/safeio.go`). |
| **V: Single Source of Truth** | PARTIAL | One bank and one eligibility rule (`kbsync.Eligible`) for every Vani outlet. Vani mirrors rkb's review hash and selection defaults rather than importing them, held together by the real-bank oracle test, which runs only when its environment is set. |
| **VI: Memory & Allocator Symmetry** | N/A | Pure Go; no manual memory management. |
| **VII: Simplicity Limit (<= 3 modules)** | PARTIAL | rkb is the third Go module in the repository (root, `decide`, `rkb`), at the limit. |
| **VIII: Anti-Abstraction** | PASSED | Standard library only; commands are plain functions behind one dispatch table. |
| **IX: Observability & Auditability** | PASSED | `finalize` writes `manifest.json` with every check and the hash of every input and output; reader verdicts are recorded by `rkb review`, and stale ones are refused. |

## 3. Complexity Tracking

| Item | Why it is here |
| :--- | :--- |
| Mirrored rule in `internal/kbsync/eligible.go` | Vani reads rkb's outputs without importing rkb (a `package main`); the oracle test `internal/kbsync/realbank_test.go` is the guard against drift. |
| Fixed 100-second fit bound in `internal/rkbresume/rkbresume.go` | Recorded as failing under heavy host load in `docs/FOLLOWUPS.md`; re-budgeting it is open (`tasks.md` Phase 5). |
