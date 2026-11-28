# Quickstart: 001 - Listings Pipeline and Decision Rules

Set `VANI_NO_KEYCHAIN=1` in any agent shell: without it a `vani` that builds a decision backend reads the owner's OpenRouter key from the Keychain and spends from it.

## Verification Commands

Validate the spec set (needs the `devcouncil` binary; fails without it):

```bash
/Users/bharath/Code/apps/Vani/scripts/test.sh --suite specs
```

That runs `devcouncil spec validate --all --strict` in the repository root.

Focused Go checks for this spec:

```bash
go -C /Users/bharath/Code/apps/Vani test ./internal/rule7/ ./internal/egress/ ./internal/ledger/ ./internal/daemon/
GOWORK=off go -C /Users/bharath/Code/apps/Vani/decide test ./...
```

The whole gate, as the release runs it (long; takes the machine's cores):

```bash
/Users/bharath/Code/apps/Vani/scripts/test.sh --fresh
```

`scripts/test.sh status` prints the newest run's summary.
