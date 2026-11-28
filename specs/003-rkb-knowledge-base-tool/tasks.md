# Tasks: 003 - rkb Knowledge-Base Tool and Eligibility Gate

## Phase 1: A Generic, Stdlib-Only Tool (Shipped)
- [x] [T-001] [US1] Embed a generic default config and layer one user file over it, stopping on an unreadable or unparsable user file `rkb/assets/config.json` `rkb/README.md` `rkb/config_test.go` `rkb/w3a_config_test.go` tests: GOWORK=off go -C /Users/bharath/Code/apps/Vani/rkb test ./...
- [x] [T-002] [P] [US1] Keep owner vocabulary in config keys and fixtures synthetic `rkb/personal_rules_test.go` `rkb/examples/README.md` `rkb/testdata`
- [x] [T-003] [P] [US1] Build the module on its own with the standard library only `rkb/go.mod` `scripts/test.sh`
- [x] [T-004] [P] [US1] Dispatch every command through one flag table that refuses unknown and repeated flags `rkb/main.go`
- [x] [T-005] [P] [US1] Check the setup (config in use, repos, claims file, bank, tools, binary) `rkb/doctor.go` `rkb/doctor_source_test.go`

## Phase 2: Safe Persistence and Bounded Subprocesses (Shipped)
- [x] [T-006] [US2] Replace every rewritten file atomically through a fsynced temp file and one rename `rkb/safeio.go` `rkb/w3a_atomic_test.go`
- [x] [T-007] [P] [US2] Serialise writers with an advisory lock that is never deleted, and refuse a write over bytes changed since they were read `rkb/safeio.go` `rkb/lock_unix.go` `rkb/lock_other.go` `rkb/harden_test.go`
- [x] [T-008] [P] [US2] Keep the previous validator report when a run crashes `rkb/claims.go` `rkb/w3a_verify_test.go`
- [x] [T-009] [P] [US2] Bound every subprocess and kill whole process groups on interrupt `rkb/proc.go` `rkb/proc_unix.go` `rkb/signals.go` `rkb/w3a_proc_test.go` `rkb/w3a_signal_test.go`
- [x] [T-010] [P] [US2] Pin the bytes rkb writes with golden files across the JSON v2 migration `rkb/json_contract_test.go` `rkb/testdata/json-contract/bank.golden.json` `rkb/testdata/json-contract/verifylog.golden.json`

## Phase 3: Verification and Release (Shipped)
- [x] [T-011] [US3] Release through one fail-closed command with eight ordered checks, staged output and a manifest of input and output hashes, stopping when an input cannot be hashed `rkb/finalize.go` `rkb/w3a_stage_test.go` `rkb/w3a_hashfail_test.go`
- [x] [T-012] [P] [US3] Write ungated drafts with unverified figures highlighted `rkb/finalize.go` `rkb/render.go`
- [x] [T-013] [P] [US3] Render HTML, fit pages, check the ATS text layer, and export Word and LaTeX `rkb/render.go` `rkb/fit.go` `rkb/ats.go` `rkb/docx.go` `rkb/latex.go` `rkb/fit_measure_test.go`
- [x] [T-014] [P] [US3] Run blind verification passes and check quoted evidence on ingest `rkb/claims.go` `rkb/quote.go` `rkb/numbers.go` `rkb/w2a_numbers_test.go`
- [x] [T-015] [P] [US3] Verify ecosystem links and knowledge figures against the claims file `rkb/eco.go` `rkb/kbcheck.go`

## Phase 4: Words and the Vani Eligibility Gate (Shipped)
- [x] [T-016] [US5] Lint bullets with configurable anti-vanity, jargon, duplicate-phrase and mechanism rules `rkb/lint.go` `rkb/guards.go` `rkb/repeats.go` `rkb/guards_test.go` `rkb/harden_test.go` `rkb/repeats_test.go`
- [x] [T-017] [US4] Apply one eligibility rule for every Vani outlet: current selection, resolved tokens, no unnamed CAUTION token, two reader roles passing the exact wording `internal/kbsync/eligible.go` `internal/kbsync/eligible_test.go` `internal/kbsync/eligible_redteam_test.go` tests: go -C /Users/bharath/Code/apps/Vani test ./internal/kbsync/
- [x] [T-018] [P] [US4] Hold the mirrored rule to rkb with a real-bank oracle test `internal/kbsync/realbank_test.go`
- [x] [T-019] [P] [US4] Run rkb bounded in time and output and stop the whole process tree on timeout or cancel `internal/rkbexec/runner.go` `internal/rkbexec/procgroup_unix.go` `internal/rkbexec/procgroup_hostile_test.go` tests: go -C /Users/bharath/Code/apps/Vani test ./internal/rkbexec/
- [x] [T-020] [P] [US4] Build per-job resumes through rkb from eligible bullets only `internal/rkbresume/rkbresume.go` `internal/rkbresume/build.go` `internal/rkbresume/build_redteam_test.go`

## Phase 5: Open Work Recorded in docs/FOLLOWUPS.md
- [ ] [T-021] [US4] Re-budget the fixed 100-second fit bound inside the 360-second resume deadline so a fit under heavy host load does not fail the build `internal/rkbresume/rkbresume.go` `docs/FOLLOWUPS.md`
- [ ] [T-022] [US4] Record the owner's decision on the reader gate for drafts and ranking (keep two rounds everywhere, or one reviewed pass outside resumes); a decision, not code `docs/FOLLOWUPS.md` `internal/kbsync/eligible.go`
- [ ] [T-023] [P] [US4] Run the real-bank oracle with its environment set and record the result; a default run skips it `internal/kbsync/realbank_test.go` `docs/TESTING.md`
