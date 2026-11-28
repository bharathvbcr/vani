# Tasks: 001 - Listings Pipeline and Decision Rules

## Phase 1: Egress and sources (Shipped)
- [x] [T-001] [US1] Send every request through one egress Doer that enforces https, the host allowlist and a private-address block on every hop `internal/egress/doer.go` `internal/egress/client.go` tests: go -C /Users/bharath/Code/apps/Vani test ./internal/egress/
- [x] [T-002] [P] [US1] Support a CONNECT proxy with paced dialing without weakening the address rules `internal/egress/proxy.go` `internal/egress/proxy_dial.go` tests: go -C /Users/bharath/Code/apps/Vani test ./internal/egress/
- [x] [T-003] [P] [US1] Give each board type its own adapter package over one source contract `internal/sources/source.go` `internal/sources/greenhouse` `internal/sources/mcphttp`

## Phase 2: Ledger and spool (Shipped)
- [x] [T-004] [US2] Append immutable JSONL events and fold them to records, with derived indexes rebuilt on start `internal/ledger/types.go` `internal/ledger/file.go` `internal/ledger/reader.go` tests: go -C /Users/bharath/Code/apps/Vani test ./internal/ledger/
- [x] [T-005] [P] [US2] Validate scraped jobs and spool them for the ledger's lock holder `internal/inbox/inbox.go` `docs/EXTENSION_INGEST.md`
- [x] [T-006] [US2] End the daemon on a fatal job error and bound a missed run to a six-hour window `internal/daemon/daemon.go` tests: go -C /Users/bharath/Code/apps/Vani test ./internal/daemon/
- [x] [T-007] [P] [US2] Render the launchd agent definition `internal/daemon/plist.go`

## Phase 3: Decision rules (Shipped)
- [x] [T-008] [US3] Keep approve, allow and notify packages from importing the decision layer, and prove the check detects a violation `internal/rule7/boundary_test.go` tests: go -C /Users/bharath/Code/apps/Vani test ./internal/rule7/
- [x] [T-009] [P] [US3] Validate that an answered value is one of the caller's options, in a stdlib-only shared module `decide/validate.go` `decide/types.go` `decide/boundary_test.go` tests: go -C /Users/bharath/Code/apps/Vani/decide test ./...
- [x] [T-010] [P] [US3] Route a question across ordered backends with a shadow backend and a total deadline `decide/router.go` `decide/router_test.go`
- [x] [T-011] [US3] Pin the triage training fixture by hash `internal/decide/jdtriage/fixture.json` `internal/decide/jdtriage/jdtriage.go` `internal/decide/jdtriage/jdtriage_test.go` tests: go -C /Users/bharath/Code/apps/Vani test ./internal/decide/jdtriage/
- [x] [T-012] [P] [US3] Keep an uncertain reading and fail open `internal/pipeline/failopen.go` `internal/pipeline/dropcandidates.go`
- [x] [T-013] [US4] Charge spend before trusting a reply and enforce a daily cap `decide/spend.go` `decide/spend_test.go` tests: go -C /Users/bharath/Code/apps/Vani/decide test ./...

## Phase 4: Pipeline stages (Shipped)
- [x] [T-014] [US5] Reconcile listings against the ledger, advance postings through triage, score and explain, and write the digest `internal/pipeline/reconcile.go` `internal/pipeline/advance.go` `internal/pipeline/triagestep.go` `internal/digest/digest.go`
- [x] [T-015] [P] [US5] Apply config exclusions without a model and reopen what no longer matches `internal/config/exclude.go` `internal/pipeline/exclude.go`
- [x] [T-016] [P] [US5] Close a removed board's postings on the next run `internal/pipeline/removedboard.go`

## Phase 5: Gate runner (Shipped)
- [x] [T-017] [US5] Shard the large cmd/vani suite and fail if a listed test was not reported `scripts/test.sh` `docs/TESTING.md` tests: go -C /Users/bharath/Code/apps/Vani test ./cmd/vani/ -run TestTestScript
- [x] [T-018] [P] [US5] Run the Go suite from a fresh clone on Linux and macOS `.github/workflows/fresh-install.yml` `scripts/bootstrap.sh`
- [x] [T-019] [US5] Add a specs suite to the local gate that runs "devcouncil spec validate --all --strict" and fails when the binary is missing `scripts/test.sh`

## Phase 6: Open work recorded in docs/FOLLOWUPS.md
- [ ] [T-020] [US1] Run "vani run-once --list-only --no-digest --board zr:ziprecruiter --board dc:dice" through Vani's own egress on the owner's Mac; the ZipRecruiter and Dice adapters were run live only through a proxy-aware stand-in (FOLLOWUPS, 2026-11-25) `docs/FOLLOWUPS.md` depends_on: [T-003]
- [ ] [T-021] [US5] Verify the Adzuna adapter with a real key (response shape, redirect link stability, count exactness, free-tier limits) `internal/sources/adzuna` `docs/FOLLOWUPS.md` depends_on: [T-003]
- [ ] [T-022] [US5] Compile and test the Swift changes recorded as not compiled in the build container `macos/Package.swift` `docs/FOLLOWUPS.md`
- [ ] [T-023] Add a Go test for the specs suite and document it as the fifth suite, and add a specs case to the per-suite failure messages `cmd/vani/test_script_test.go` `docs/TESTING.md` `scripts/release.sh` depends_on: [T-019]
