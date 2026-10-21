#!/bin/sh
# The extension's type-check gate. `bun test` does not type-check, so this is the only thing that does.
#
#   bun install --frozen-lockfile   (installs the exact-pinned compiler from bun.lock; fails on drift)
#   ./node_modules/.bin/tsc -p tsconfig.json --noEmit
#
# Run from extension/:  sh scripts/typecheck.sh   (or `bun run typecheck`).
# Never `bunx tsc`: bunx downloads a missing package, so an absent compiler could pass.
# VANI_TSC overrides the compiler path (the tests use it to exercise the missing-binary path).
set -u
here=$(dirname "$0")/..
cd "$here" || exit 2
tsc="${VANI_TSC:-./node_modules/.bin/tsc}"
if [ -z "${VANI_SKIP_INSTALL:-}" ]; then
  bun install --frozen-lockfile || { echo "typecheck: FAILED: bun install --frozen-lockfile did not succeed" >&2; exit 1; }
fi
if [ ! -f "$tsc" ] || [ ! -x "$tsc" ]; then
  echo "typecheck: FAILED: the TypeScript compiler $tsc is not installed; run 'bun install --frozen-lockfile' in extension/ (this gate never reports ok without running tsc)" >&2
  exit 1
fi
"$tsc" -p tsconfig.json --noEmit || { echo "typecheck: FAILED: tsc reported errors" >&2; exit 1; }
echo "typecheck: ok"
