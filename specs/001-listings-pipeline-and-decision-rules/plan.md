# Implementation Plan: 001 - Listings Pipeline and Decision Rules

## 1. Technical Context

- **Language**: Go only for the pipeline (`go.mod` at the repository root). Rules from `CLAUDE.md`: standard library, Manvi and the owner's `decide` module; no new third-party dependency without asking the owner; `go.mod` and `go.work` are not edited by agents.
- **Modules**: the root module `github.com/bharathvbcr/Vani`, the nested stdlib-only module `decide/` (`replace`d to `./decide`), and the nested `rkb/` (spec 003). The root `./...` does not reach nested modules; `scripts/test.sh` tests each separately with `GOWORK=off`.
- **Entry points**: `cmd/vani` (`main.go` holds the usage text; `vani run-once`, `daemon`, `run-request`, `jobs ...`, `watchlist ...`, `status`, `runs ...`, `digest`). Scheduling on macOS is a launchd agent rendered by `internal/daemon/plist.go`.
- **Packages**: `internal/sources/*` (one adapter per ATS or board, plus `srcutil`, `mcphttp`, `htmltext`, `jsonld`), `internal/egress`, `internal/posting`, `internal/ledger`, `internal/pipeline`, `internal/config`, `internal/inbox`, `internal/digest`, `internal/daemon`, `internal/decide/{jdtriage,llmbackend,jdreasons,kbmatch,fieldtriage,...}`, `internal/rule7`.
- **Persistence**: append-only JSONL ledger under the state directory (`internal/ledger`), an inbox spool of batch files, run records under `<state>/runs/history/`. No database.
- **Config and state location**: one pair per process, resolved by `internal/config/appsettings.go` (`Resolve`); `VANI_CONFIG` and `VANI_STATE_DIR` win and must be absolute. The mechanism is documented in `docs/RUNTIME.md`; the owner's live values are deliberately not recorded here.
- **Gate**: `scripts/test.sh` is the one local entry point (suites `go`, `swift`, `bun`, `types`, `specs`); hosted CI (`.github/workflows/fresh-install.yml`) runs `scripts/test.sh --suite go --fresh` from a fresh clone set up by `scripts/bootstrap.sh`, on Linux and macOS.
- **Code intelligence**: DevMap store `.devcouncil/codeintel/devmap.sqlite` (reported degraded when this spec was written, see `research.md`).

## 2. Constitution Check & Complexity Tracking

Judged against the nine articles of DevCouncil's `constitution.md`, which is DevCouncil-specific. Vani's own principles are the Rules in `CLAUDE.md` and Rule 7 (`decide/README.md`); there is no separate principles document, so no repo-local constitution was created.

| Article | Status | Compliance Details / Justification |
| :--- | :--- | :--- |
| **I: Library-First** | PASSED | Behaviour lives in `internal/*` packages and the `decide` module; `cmd/vani` is the thin binary over them. |
| **II: CLI as First-Class Consumer** | PASSED | Every capability has a `vani` subcommand with `--json`; the Mac app is itself a client of `vani jobs ... --json` (`macos/README.md`). |
| **III: Test-First** | PASSED | `CLAUDE.md` requires every behaviour to ship with tests including adversarial inputs; attack and adversarial test files are throughout `internal/*` and `cmd/vani`. Not re-run here. |
| **IV: Fail-Closed Gates** | PASSED | Egress refuses by default; an uncertain reading keeps the posting; `scripts/test.sh` never reports a skipped or unreported test as a plain pass; a stuck ledger ends the daemon (`daemon.ErrFatal`). |
| **V: Single Source of Truth** | PASSED | `internal/egress` is the only outbound path; the ledger is the only record and indexes are derived; `internal/decide/jdtriage/fixture.json` is pinned by hash. |
| **VI: Memory & Allocator Symmetry** | N/A | Pure Go in this spec; the Metal embeddings path (`-tags tessl`) is outside it. |
| **VII: Simplicity Limit (<= 3 modules)** | PARTIAL | Three Go modules (root, `decide`, `rkb`), at the limit. The adapter count is large by design (one package per board). |
| **VIII: Anti-Abstraction** | PASSED | Adapters are plain packages against one `Doer` interface and one `source.go` contract; no plugin framework. |
| **IX: Observability & Auditability** | PASSED | Every ledger event carries a `reason`; every finished run keeps a record in `<state>/runs/history/` (`vani runs list`). |

## 3. Complexity Tracking

- `cmd/vani` holds a very large number of tests that run in one process; `scripts/test.sh` shards them. That is a cost of the single-binary layout, recorded in `docs/TESTING.md`, not a proposal to change it.
- The `decide` module is shared with DevCouncil and n8n-email-engine; unifying the DevCouncil copy of the Lappi client is an open follow-up in `docs/FOLLOWUPS.md`.
