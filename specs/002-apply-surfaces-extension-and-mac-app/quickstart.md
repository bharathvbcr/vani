# Quickstart: 002 - Apply Surfaces: Browser Extension and Mac App

## Verification Commands

Validate the spec set (needs the `devcouncil` binary; fails without it):

```bash
/Users/bharath/Code/apps/Vani/scripts/test.sh --suite specs
```

Extension tests and type-check (Bun required; no network):

```bash
/Users/bharath/Code/apps/Vani/scripts/test.sh --suite bun,types
```

Swift tests (Mac only; the window-driving UI tests are not run without `--ui` because they take the keyboard):

```bash
/Users/bharath/Code/apps/Vani/scripts/test.sh --suite swift
```

Build the Chrome extension for manual loading (see `extension/README.md` for the Load unpacked steps):

```bash
bun run /Users/bharath/Code/apps/Vani/extension/build.ts
```

Set `VANI_NO_KEYCHAIN=1` before running any `vani` command from an agent shell.
