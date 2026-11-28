# Technical Research & Alternatives

## 1. Options Evaluated

- **Own egress versus Manvi's `manvi/fetch`.** `internal/egress/doer.go` explains why Vani keeps its own `Doer`: Manvi's fetch only issues GET, reduces bodies to prose and caps them for a model context, while ATS APIs need raw JSON and Workday needs POST. The rules mirror Manvi's; converging on an exported Manvi policy is a tracked follow-up (`docs/FOLLOWUPS.md`), so the duplication is acknowledged.
- **Spool versus a second ledger writer.** The ledger takes one writer, and a daemon holds its exclusive lock for its whole life, so extension-scraped jobs go to an inbox spool drained by the lock holder (`docs/EXTENSION_INGEST.md`) instead of a lock handoff.
- **Append-only JSONL with derived indexes** over a database: stated in `internal/ledger/types.go`; crash safety is tested in `internal/ledger/durability_test.go` and `internal/ledger/dirsync_test.go`.
- **Model answers annotate; engine rules drop.** Rule 7 keeps the model out of any allow or drop decision, which is enforced structurally by an import-boundary test rather than by review (`internal/rule7/boundary_test.go`).
- **Spec gate placement.** A `specs` suite was added to `scripts/test.sh`, the one local entry point that `scripts/release.sh` calls, rather than a new script. Hosted CI runs only `--suite go`, so it is unaffected.

## 2. Evidence Gathered While Writing This Spec

- Constants and statuses were read from source in this session: the status list in `internal/posting/posting.go`, `MissedRunWindow` and `wakeSlice` in `internal/daemon/daemon.go`, `fixtureSHA256` in `internal/decide/jdtriage/jdtriage_test.go`, the `ErrScheme` error in `internal/egress/client.go`, the `gatedPackages` map in `internal/rule7/boundary_test.go`.
- Every path cited in `tasks.md` was checked to exist with `test -e`.
- `cmd/vani/test_script_test.go` was read in full before editing `scripts/test.sh`: its tests always pass an explicit `--suite` (or `status`, `--print-skip-classes`), and the "unknown suite" case only checks exit 2, so adding a default suite does not change any assertion there. That test file was not edited and not run.

## 3. Unverified

- **Nothing was executed**: no Go test, vet or build, no `vani` command. The pass state of every suite named in the metrics is unknown. The tree holds other sessions' uncommitted edits (`rkb/quote.go`, `rkb/w2a_numbers_test.go` and a set of `extension/src` files at the time of writing), so a run here would not measure `HEAD`.
- **DevMap was degraded**: `waylode_insights` reported `analyzer_freshness: false` ("stored extraction payload is obsolete; rebuild with the current analyzer") for this repository, so graph queries were not used to confirm callers or call sites. Paths and symbols were confirmed by reading source with `rg` and `sed`; no claim here relies on the graph.
- Whether `internal/pipeline` stage order matches the status list was read from file names and the status constants, not traced through `run.go`.
- How many ATS adapters are live and which boards the owner watches is configuration and was not read.
- The open items in `tasks.md` Phase 6 are copied from the dated section "Job boards: ZipRecruiter, Dice, Adzuna and the Indeed skill (2026-11-25)" of `docs/FOLLOWUPS.md`. Later work may have closed them; that was not checked.
- Linux and Windows behaviour was not checked; the README states macOS-only desktop pieces.
- Adding `specs` to the default suites of `scripts/test.sh` means `scripts/release.sh` now also runs it. `release.sh` has no case for it in its per-suite failure messages (it falls through to the generic "tests-failed" message). `docs/TESTING.md` still describes four suites, and no Go test covers the new suite; the test file is outside this task's allowed edits.
