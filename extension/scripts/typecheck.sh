#!/bin/sh
# The extension's type-check gate. `bun test` does not type-check, so this is the only thing that does.
#
#   bun install --frozen-lockfile   (installs the exact-pinned compiler from bun.lock; fails on drift)
#   ./node_modules/.bin/tsc --version   (must name the version pinned in package.json: a truncated or foreign binary is not a compiler)
#   ./node_modules/.bin/tsc -p tsconfig.json --noEmit
#
# Run from extension/:  sh scripts/typecheck.sh   (or `bun run typecheck`).
# Never `bunx tsc`: bunx downloads a missing package, so an absent compiler could pass.
# There are no overrides: "typecheck: ok" is printed only after the pinned compiler ran and passed. The tests run a
# copy of this script in a scratch directory with their own node_modules and a stub `bun` on PATH.
set -u
here=$(dirname "$0")/..
cd "$here" || exit 2
tsc=./node_modules/.bin/tsc
bun install --frozen-lockfile || { echo "typecheck: FAILED: bun install --frozen-lockfile did not succeed" >&2; exit 1; }
if [ ! -f "$tsc" ] || [ ! -x "$tsc" ]; then
  echo "typecheck: FAILED: the TypeScript compiler $tsc is not installed; run 'bun install --frozen-lockfile' in extension/ (this gate never reports ok without running tsc)" >&2
  exit 1
fi
pinned=$(sed -n 's/.*"typescript"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' package.json | head -n 1)
if [ -z "$pinned" ]; then
  echo "typecheck: FAILED: package.json pins no typescript devDependency" >&2
  exit 1
fi
got=$("$tsc" --version 2>/dev/null)
if [ "$got" != "Version $pinned" ]; then
  echo "typecheck: FAILED: $tsc reports '$got', not 'Version $pinned' (the version pinned in package.json); run 'bun install --frozen-lockfile' in extension/" >&2
  exit 1
fi
"$tsc" -p tsconfig.json --noEmit || { echo "typecheck: FAILED: tsc reported errors" >&2; exit 1; }
echo "typecheck: ok"
