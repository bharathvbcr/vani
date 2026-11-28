# Verification Plan: 003 - rkb Knowledge-Base Tool and Eligibility Gate

## Deterministic Verification Matrix

| Check | Command | Expected Outcome | Run during authoring |
| :--- | :--- | :--- | :--- |
| Spec schema and strictness | `devcouncil spec validate --all --strict --json --project-root /Users/bharath/Code/apps/Vani` | `valid: true`, no blocking findings | yes, passed (2026-11-28) |
| Cited paths exist | `test -e` over every backticked path in `tasks.md` | all exist | yes |
| rkb module tests (SC-001) | `GOWORK=off go -C /Users/bharath/Code/apps/Vani/rkb test ./...` | exit 0 | earlier session only (exit 0, 38 s, on the dirty working tree); not re-run, host under memory pressure |
| rkb vet | `GOWORK=off go -C /Users/bharath/Code/apps/Vani/rkb vet ./...` | exit 0 | no |
| Nested module in the gate (SC-002) | `/Users/bharath/Code/apps/Vani/scripts/test.sh --suite go` | `pass`, with `rkb` tested on its own (`GO_NESTED`) | no |
| Eligibility and runner | `go -C /Users/bharath/Code/apps/Vani test ./internal/kbsync/ ./internal/rkbexec/ ./internal/rkbresume/` | exit 0 | no |
| Real-bank oracle | `KBSYNC_REAL_BANK=1` with `KBSYNC_BANK`, `KBSYNC_RKB`, `KBSYNC_CODE_ROOT` set, then `go -C /Users/bharath/Code/apps/Vani test ./internal/kbsync/ -run RealBank` | passes; without the variables it skips | no (open as T-023) |
| Tool setup | `rkb doctor` | config in use and every tool found | no |

## Not Verified Here

- No test ran in this session.
- The real-bank oracle needs the owner's career files, which are outside this repository; a default run skips it.
- `rkb finalize` needs Chrome, poppler and optionally LibreOffice and `pdflatex`; none was exercised.
