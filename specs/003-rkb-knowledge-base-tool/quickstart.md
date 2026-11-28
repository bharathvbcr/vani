# Quickstart: 003 - rkb Knowledge-Base Tool and Eligibility Gate

## Verification Commands

Validate the spec set (needs the `devcouncil` binary; fails without it):

```bash
/Users/bharath/Code/apps/Vani/scripts/test.sh --suite specs
```

Build, vet and test rkb on its own (the repository's `go.work` does not include it):

```bash
GOWORK=off go -C /Users/bharath/Code/apps/Vani/rkb build -o ../build/rkb .
GOWORK=off go -C /Users/bharath/Code/apps/Vani/rkb vet ./...
GOWORK=off go -C /Users/bharath/Code/apps/Vani/rkb test ./...
```

Vani-side consumers:

```bash
go -C /Users/bharath/Code/apps/Vani test ./internal/kbsync/ ./internal/rkbexec/ ./internal/rkbresume/
```

The real-bank oracle runs only with its environment set (`KBSYNC_REAL_BANK=1` plus `KBSYNC_BANK`, `KBSYNC_RKB` and `KBSYNC_CODE_ROOT`); a default run skips it.

Check the tool's setup (config in use, repos, `Claims.tex`, bank, log, external tools):

```bash
/Users/bharath/Code/apps/Vani/build/rkb doctor
```

Set `VANI_NO_KEYCHAIN=1` before running any `vani` command from an agent shell (`CLAUDE.md`).
