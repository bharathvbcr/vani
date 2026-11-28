# Technical Research & Alternatives

## 1. Options Evaluated

These are the choices the repository records; this spec did not re-decide them.

- **Nested module inside Vani versus a separate repository.** rkb is the nested module `rkb/` with its own `go.mod`, built with `GOWORK=off` and tested separately by `scripts/test.sh` (`CLAUDE.md`, `rkb/README.md`). Vani runs it as a binary rather than importing it.
- **Generic defaults plus one user file.** Owner vocabulary (codenames, jargon lists, banned phrasings, mechanisms, repo map) lives in config, so the module carries no personal data and its fixtures stay synthetic (`rkb/README.md` "Configuration"; `CLAUDE.md`).
- **Atomic write, advisory lock and compare-and-swap.** A crash or full disk must not leave a half-written bank; the lock file is never deleted because some mounts refuse deletes; an edit by a tool that does not take the lock is detected by the sha256 of the bytes read (`rkb/safeio.go` header).
- **One release command.** `finalize` runs the checks in a fixed order and stops at the first failure, stages output, publishes, and writes a manifest; `build` makes ungated drafts (`rkb/finalize.go`).
- **Mirror versus import in Vani.** Vani mirrors rkb's review hash and selection defaults in `internal/kbsync/eligible.go` instead of importing them (rkb is a `package main`), and holds the mirror to rkb with the real-bank oracle test.
- **JSON v2 with a byte contract.** rkb moved to `encoding/json/jsontext`; golden files written by the v1 build pin the output bytes so the migration cannot reorder, re-escape or re-null anything (`rkb/json_contract_test.go`).
- **Spec gate placement.** The `specs` suite of `scripts/test.sh` runs `devcouncil spec validate --all --strict`; hosted CI does not run it.

## 2. Evidence Gathered While Writing This Spec

- Read in this session: `rkb/README.md`, the usage text and flag tables in `rkb/main.go`, the header of `rkb/finalize.go` and its `Manifest`, the header of `rkb/safeio.go`, the type definitions in `rkb/bank.go`, `rkb/claims.go` and `rkb/render.go`, the header of `rkb/quote.go`, the eligibility comment in `internal/kbsync/eligible.go`, the `run_specs` function and `GO_NESTED` in `scripts/test.sh`, and the rkb sections of `docs/FOLLOWUPS.md`.
- Every path cited in `tasks.md` was checked with `test -e`.
- Two follow-ups listed in `docs/FOLLOWUPS.md` were found already done in code and are not listed as open: `rkbresume.Runner` is now `rkbexec.Runner` (`internal/rkbresume/rkbresume.go`), and `kb.vm_report` is a config key (`VMReport` in `internal/config/config.go`) read by `cmd/vani/resumecmd.go`. The 100-second fit bound inside the 360-second deadline is still in `internal/rkbresume/rkbresume.go`, so T-021 is open.
- DevMap: `devmap status` reported the store fresh at `f23a7a99`, query-ready, partial (5 files recovered by pattern). `devmap search Eligible` located `Eligible` in `internal/kbsync/eligible.go` and its caller file `cmd/vani/eligiblebank.go` (65 matches, truncated at 19 shown). The rkb module itself was read directly with `rg` and `sed`.

## 3. Unverified

- **Test runs.** An earlier session recorded `GOWORK=off go -C rkb test ./...` exiting 0 in 38 seconds on this working tree, with other sessions' uncommitted edits to `rkb/quote.go` and `rkb/w2a_numbers_test.go` present (SC-001 in `spec.md`). It was not re-run here because the host was under memory pressure, and it was never run on `HEAD`.
- **The real-bank oracle.** `internal/kbsync/realbank_test.go` skips unless `KBSYNC_REAL_BANK=1` and its paths are set. Whether `scripts/test.sh` reports that skip is inferred, not verified: its skip counting covers the Swift serial pass and the `cmd/vani` shards, and no skip count for `internal/kbsync` was seen. Treat a green Go suite as not having run the oracle.
- **`rkb doctor`** was not run.
- **The career repository's own pipeline**, which also uses an `rkb` binary, was not read; whether it runs this module's build is not established here.
