# Verification Plan: 001 - Listings Pipeline and Decision Rules

## Deterministic Verification Matrix

| Check | Command | Expected Outcome | Run during authoring |
| :--- | :--- | :--- | :--- |
| Spec schema and strictness | `devcouncil spec validate --all --strict --json --project-root /Users/bharath/Code/apps/Vani` | `valid: true`, no blocking findings | yes, passed (2026-11-28) |
| Cited paths exist | `test -e` over every backticked path in `tasks.md` | all exist | yes |
| Spec gate wiring | `rg -n 'run_specs' scripts/test.sh` | a `specs` suite that fails when `devcouncil` is missing | yes (read) |
| Boundary, egress, ledger, daemon (SC-001) | `go -C /Users/bharath/Code/apps/Vani test ./internal/rule7/ ./internal/egress/ ./internal/ledger/ ./internal/daemon/` | exit 0 | no |
| Shared decide module (SC-002) | `GOWORK=off go -C /Users/bharath/Code/apps/Vani/decide test ./...` | exit 0 | no |
| Triage fixture pin (SC-003) | `go -C /Users/bharath/Code/apps/Vani test ./internal/decide/jdtriage/` | exit 0 | no |
| Go suite with shard accounting (SC-005) | `/Users/bharath/Code/apps/Vani/scripts/test.sh --suite go` | `pass`; an unreported `cmd/vani` test is a failure | no |
| Spec suite (SC-006) | `/Users/bharath/Code/apps/Vani/scripts/test.sh --suite specs` | `pass` | no (the same command was run directly, row 1) |

## Not Verified Here

- No Go test ran in this session; the host was under memory pressure. Set `VANI_NO_KEYCHAIN=1` before any run that builds a `vani` binary.
- Live board runs (T-020, T-021 in `tasks.md`) need the owner's Mac and keys.
