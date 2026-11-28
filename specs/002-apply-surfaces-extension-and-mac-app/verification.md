# Verification Plan: 002 - Apply Surfaces: Browser Extension and Mac App

## Deterministic Verification Matrix

| Check | Command | Expected Outcome | Run during authoring |
| :--- | :--- | :--- | :--- |
| Spec schema and strictness | `devcouncil spec validate --all --strict --json --project-root /Users/bharath/Code/apps/Vani` | `valid: true`, no blocking findings | yes, passed (2026-11-28) |
| Cited paths exist | `test -e` over every backticked path in `tasks.md` | all exist | yes |
| Extension tests (SC-001) | `/Users/bharath/Code/apps/Vani/scripts/test.sh --suite bun` | `pass`; a failure that vanishes serially is reported and fails under `--strict` | no |
| Extension type-check (SC-002) | `/Users/bharath/Code/apps/Vani/scripts/test.sh --suite types` | `pass` | no |
| Swift tests with skip accounting (SC-003) | `/Users/bharath/Code/apps/Vani/scripts/test.sh --suite swift` | `pass`; skips counted; UI tests NOT RUN without `--ui` | no |
| Runner self-tests for these suites | `go -C /Users/bharath/Code/apps/Vani test ./cmd/vani/ -run 'TestTestScript'` | exit 0 | no |

## Not Verified Here

- No Bun, Swift or Go test ran in this session; the host was under memory pressure, and about 20 extension files carry uncommitted edits from other sessions.
- The Bun, types and Swift suites run only on the owner's Mac; hosted CI does not run them (SC-006).
- The Safari path in Safari itself and the Swift files changed without a toolchain remain open (T-017, T-018 in `tasks.md`).
