#!/bin/bash
# From a fresh clone of Vani to a built `vani` and `rkb` and a checked config, in one command:
#
#   scripts/bootstrap.sh [--deps DIR] [--no-build] [--no-init]
#
#   1. checks git and Go (the version go.mod names; with GOTOOLCHAIN=auto, Go downloads it itself);
#   2. clones the four public sibling repos Vani builds against (Manvi, DevCouncil, gusset, tessl) into DIR
#      (default: .deps/ in this checkout, gitignored) at the commits pinned in scripts/deps.lock. A checkout
#      that is already there is never changed: one at another commit is reported, not moved;
#   3. writes go.work pointing at them. An existing go.work is never touched: the clone step is skipped and
#      the modules it names are only checked;
#   4. confirms every module resolves, then builds build/vani and build/rkb (--no-build stops once they
#      resolve);
#   5. writes the default config with `vani init` when there is none (--no-init skips it), then runs
#      `vani config check`;
#   6. reports each optional layer (OpenRouter key, DevPrism, Chrome and poppler for rkb's PDFs, the Tessl
#      engine, the embedding snapshot, lightpanda) as found, missing or not checked, with the next step.
#
# It never reads or prints a secret (the Keychain check asks only whether the item exists), never edits an
# existing checkout, go.work or config, and calls no model, so it spends nothing.
#
# Test seams (tests only; leave them unset):
#   VANI_BOOTSTRAP_GIT   git executable (default: git)
#   VANI_BOOTSTRAP_GO    go executable  (default: go)
#   VANI_BOOTSTRAP_ROOT  repo root      (default: the repo holding this script)
#   VANI_BOOTSTRAP_LOCK  lock file      (default: <root>/scripts/deps.lock)
set -uo pipefail

USAGE="usage: scripts/bootstrap.sh [--deps DIR] [--no-build] [--no-init]"
die() { echo "bootstrap: $*" >&2; exit 1; }
usage_die() { echo "bootstrap: $*" >&2; echo "$USAGE" >&2; exit 2; }
step() { echo; echo "== $*"; }

DEPS=""
BUILD=1
INIT=1
while [ $# -gt 0 ]; do
  case "$1" in
    --deps)
      shift
      { [ $# -gt 0 ] && [ -n "$1" ]; } || usage_die "--deps needs a directory"
      DEPS="$1" ;;
    --no-build) BUILD=0 ;;
    --no-init) INIT=0 ;;
    -h|--help) echo "$USAGE"; echo "See the comment at the top of this script and docs/SETUP.md."; exit 0 ;;
    *) usage_die "unknown argument $1" ;;
  esac
  shift
done

GIT="${VANI_BOOTSTRAP_GIT:-git}"
GO="${VANI_BOOTSTRAP_GO:-go}"
ROOT="${VANI_BOOTSTRAP_ROOT:-$(cd "$(dirname "$0")/.." && pwd)}"
LOCK="${VANI_BOOTSTRAP_LOCK:-$ROOT/scripts/deps.lock}"
[ -f "$ROOT/go.mod" ] || die "no go.mod at $ROOT"
[ -f "$LOCK" ] || die "no lock file at $LOCK"

# ver_ge A B: dotted numeric version A is at least B.
ver_ge() {
  local a="$1" b="$2" x y
  while [ -n "$a" ] || [ -n "$b" ]; do
    x=${a%%.*}; y=${b%%.*}
    if [ "$a" = "$x" ]; then a=""; else a=${a#*.}; fi
    if [ "$b" = "$y" ]; then b=""; else b=${b#*.}; fi
    x=${x:-0}; y=${y:-0}
    [ "$x" -gt "$y" ] && return 0
    [ "$x" -lt "$y" ] && return 1
  done
  return 0
}

step "1/6 prerequisites"
NEED=$(awk '$1 == "go" { print $2; exit }' "$ROOT/go.mod")
[[ "$NEED" =~ ^[0-9]+(\.[0-9]+)*$ ]] || die "go.mod names no usable go version (read '$NEED')"
command -v "$GIT" >/dev/null 2>&1 || die "git not found (on macOS: xcode-select --install)"
command -v "$GO" >/dev/null 2>&1 || die "go not found: install Go $NEED or newer from https://go.dev/dl/"
echo "  git: $("$GIT" --version 2>/dev/null)"
# GOTOOLCHAIN=local reports the installed Go, not one go.mod would make it download.
RAW=$(GOTOOLCHAIN=local "$GO" env GOVERSION 2>/dev/null)
TOOLCHAIN=$("$GO" env GOTOOLCHAIN 2>/dev/null)
HAVE=$(printf '%s' "${RAW#go}" | sed 's/[^0-9.].*//')
if [ -z "$HAVE" ]; then
  echo "  go: version not read ('$RAW'); not checked here, the build below fails if it is too old"
elif ver_ge "$HAVE" "$NEED"; then
  echo "  go: $HAVE (go.mod needs $NEED)"
else
  case "$TOOLCHAIN" in
    *local*) die "Go $HAVE is older than $NEED and GOTOOLCHAIN=$TOOLCHAIN forbids downloading it: install Go $NEED from https://go.dev/dl/" ;;
    *) echo "  go: $HAVE is older than $NEED; Go downloads go$NEED on the first build (GOTOOLCHAIN=${TOOLCHAIN:-auto})" ;;
  esac
fi

# The lock: one "name url commit" per line; # comments and blank lines are skipped.
NAMES=(); URLS=(); SHAS=()
n=0
while IFS= read -r line || [ -n "$line" ]; do
  n=$((n + 1))
  line=${line%%#*}
  read -r name url sha extra <<< "$line"
  [ -n "${name:-}" ] || continue
  { [ -n "${sha:-}" ] && [ -z "${extra:-}" ]; } || die "$LOCK:$n: want 'name url commit'"
  [[ "$name" =~ ^[A-Za-z0-9._-]+$ ]] || die "$LOCK:$n: bad name '$name'"
  [[ "$url" =~ ^(https://|file://) ]] || die "$LOCK:$n: url must start with https://"
  [[ "$sha" =~ ^[0-9a-f]{40}$ ]] || die "$LOCK:$n: commit must be a full 40-character sha"
  for seen in ${NAMES[@]+"${NAMES[@]}"}; do
    [ "$seen" != "$name" ] || die "$LOCK:$n: $name is listed twice"
  done
  NAMES+=("$name"); URLS+=("$url"); SHAS+=("$sha")
done < "$LOCK"
for want in Manvi DevCouncil gusset tessl; do
  found=0
  for seen in ${NAMES[@]+"${NAMES[@]}"}; do [ "$seen" = "$want" ] && found=1; done
  [ "$found" = 1 ] || die "$LOCK does not list $want"
done

step "2/6 sibling checkouts"
if [ -f "$ROOT/go.work" ]; then
  echo "  go.work exists: using the checkouts it names, cloning nothing"
  [ -z "$DEPS" ] || echo "  (--deps $DEPS ignored: go.work already decides where the siblings are)"
else
  DEPS="${DEPS:-$ROOT/.deps}"
  mkdir -p "$DEPS" || die "cannot create $DEPS"
  DEPS=$(cd "$DEPS" && pwd) || die "cannot enter $DEPS"
  case "$DEPS" in *'"'*|*\\*) die "the deps path $DEPS holds a quote or backslash, which go.work cannot carry; pass --deps elsewhere" ;; esac
  echo "  into $DEPS"
  i=0
  while [ "$i" -lt "${#NAMES[@]}" ]; do
    name=${NAMES[$i]}; url=${URLS[$i]}; sha=${SHAS[$i]}; dir="$DEPS/$name"
    i=$((i + 1))
    if [ ! -e "$dir" ]; then
      echo "  $name: fetching ${sha:0:12} from $url"
      # GIT_TERMINAL_PROMPT=0: a repo that asks for credentials fails instead of waiting for input.
      # The low-speed pair aborts a transfer that stalls below 1 KB/s for 60 s.
      if ! { "$GIT" init -q "$dir" \
          && "$GIT" -C "$dir" remote add origin "$url" \
          && GIT_TERMINAL_PROMPT=0 GIT_HTTP_LOW_SPEED_LIMIT=1000 GIT_HTTP_LOW_SPEED_TIME=60 \
             "$GIT" -C "$dir" fetch -q --depth 1 origin "$sha" \
          && "$GIT" -C "$dir" -c advice.detachedHead=false checkout -q --detach FETCH_HEAD; }; then
        rm -rf "$dir"
        die "$name: could not fetch ${sha:0:12} from $url (the partial $dir was removed); check the network and rerun"
      fi
    elif [ -e "$dir/.git" ]; then
      head=$("$GIT" -C "$dir" rev-parse HEAD 2>/dev/null) || die "$name: $dir is not a readable git checkout"
      if [ "$head" = "$sha" ]; then
        echo "  $name: already at the locked commit"
      else
        echo "  $name: at ${head:0:12}, the lock says ${sha:0:12}; left as it is (the build below says whether it fits)"
      fi
      if [ -n "$("$GIT" -C "$dir" status --porcelain 2>/dev/null | head -1)" ]; then
        echo "  $name: has uncommitted changes; left as they are"
      fi
    else
      die "$dir exists and is not a git checkout; move it aside or pass --deps DIR"
    fi
  done

  step "3/6 go.work"
  tmp="$ROOT/go.work.bootstrap.$$"
  {
    echo "// Written by scripts/bootstrap.sh. Siblings at the commits in scripts/deps.lock."
    echo "go $NEED"
    echo
    echo "use ("
    printf '\t.\n\t./decide\n\t"%s"\n' "$DEPS/Manvi/manvi"
    echo ")"
    echo
    printf 'replace github.com/bharathvbcr/Manvi/manvi v0.0.0 => "%s"\n' "$DEPS/Manvi/manvi"
    printf 'replace github.com/bharathvbcr/tessl/go v0.0.0 => "%s"\n' "$DEPS/tessl/go"
  } > "$tmp" || { rm -f "$tmp"; die "cannot write $tmp"; }
  mv "$tmp" "$ROOT/go.work" || { rm -f "$tmp"; die "cannot move $tmp to $ROOT/go.work"; }
  echo "  wrote $ROOT/go.work"
fi

step "4/6 resolve and build"
MODS="github.com/bharathvbcr/Manvi/manvi github.com/bharathvbcr/decide github.com/bharathvbcr/tessl/go github.com/bharathvbcr/DevCouncil/backend/go_orchestrator github.com/bharathvbcr/gusset"
# shellcheck disable=SC2086
LISTED=$("$GO" -C "$ROOT" list -m -f '{{.Path}} {{.Dir}}' $MODS 2>&1) || { echo "$LISTED" >&2; die "go list could not resolve the modules (output above)"; }
TESSL_GO=""
for mod in $MODS; do
  dir=$(printf '%s\n' "$LISTED" | awk -v m="$mod" '$1 == m { print substr($0, length(m) + 2); exit }')
  [ -n "$dir" ] && [ -d "$dir" ] || die "$mod resolved to no directory ('$dir'); go.work or a checkout is wrong"
  echo "  $mod -> $dir"
  [ "$mod" = github.com/bharathvbcr/tessl/go ] && TESSL_GO="$dir"
  # decide lives in this repo. A go.work that still `use`s an outside copy wins over go.mod's replace, so
  # vani would build from that copy while the tests read this one.
  if [ "$mod" = github.com/bharathvbcr/decide ] && [ "$(cd "$dir" && pwd -P)" != "$(cd "$ROOT/decide" 2>/dev/null && pwd -P)" ]; then
    die "decide resolves to $dir, not $ROOT/decide: in go.work, replace the use line for that directory with ./decide"
  fi
done
if [ "$BUILD" = 0 ]; then
  echo "  --no-build: stopping before the build"
  exit 0
fi
mkdir -p "$ROOT/build" || die "cannot create $ROOT/build"
"$GO" -C "$ROOT" build -o "$ROOT/build/vani" ./cmd/vani || die "building vani failed (output above)"
echo "  built $ROOT/build/vani"
GOWORK=off "$GO" -C "$ROOT/rkb" build -o "$ROOT/build/rkb" . || die "building rkb failed (output above)"
echo "  built $ROOT/build/rkb"

V="$ROOT/build/vani"
CONFIG_OK=1
step "5/6 config"
if [ "$INIT" = 1 ]; then
  out=$(VANI_NO_KEYCHAIN=1 "$V" init 2>&1); rc=$?
  if [ "$rc" = 0 ]; then
    printf '%s\n' "$out" | sed 's/^/  /'
  elif printf '%s' "$out" | grep -q 'refusing to overwrite'; then
    echo "  a config exists; left as it is"
  else
    printf '%s\n' "$out" >&2
    die "vani init failed (output above)"
  fi
else
  echo "  --no-init: not writing a config"
fi
VANI_NO_KEYCHAIN=1 "$V" config check 2>&1 | sed 's/^/  /'
[ "${PIPESTATUS[0]}" = 0 ] || CONFIG_OK=0

step "6/6 optional layers (each adds a capability; the core pipeline runs without them)"
layer() { printf '  %-18s %-12s %s\n' "$1" "$2" "$3"; }
OS=$(uname -s); ARCH=$(uname -m)

if [ -n "${OPENROUTER_API_KEY:-}" ]; then
  layer "OpenRouter key" found "OPENROUTER_API_KEY is set in this shell"
elif [ "$OS" != Darwin ]; then
  layer "OpenRouter key" missing "export OPENROUTER_API_KEY=... (triage needs it, or an llm block)"
elif [ -n "${VANI_NO_KEYCHAIN:-}" ]; then
  layer "OpenRouter key" "not checked" "VANI_NO_KEYCHAIN is set, so the Keychain was not asked"
else
  security find-generic-password -s openrouter-api-key >/dev/null 2>&1; rc=$?
  case "$rc" in
    0) layer "OpenRouter key" found "Keychain item openrouter-api-key exists (not read)" ;;
    44) layer "OpenRouter key" missing "security add-generic-password -U -a \"\$USER\" -s openrouter-api-key -T $V -w" ;;
    *) layer "OpenRouter key" "not checked" "security exited $rc" ;;
  esac
fi

if [ -x "$ROOT/build/rkb" ]; then
  layer "rkb" found "$ROOT/build/rkb (a new knowledge base: kb.rkb, your lists in ~/.config/rkb/config.json, sample in rkb/examples; docs/KNOWLEDGE_BASE_GUIDE.md)"
else
  layer "rkb" missing "the build above did not produce it"
fi

DEVPRISM=/Applications/DevPrism.app/Contents/MacOS/claude-prism-desktop
if [ -x "$DEVPRISM" ]; then
  layer "DevPrism" found "$DEVPRISM (set devprism.binary to it; docs/SETUP.md step 7)"
else
  layer "DevPrism" missing "ranking is skipped without it: https://github.com/bharathvbcr/DevPrism"
fi

CHROME=""
for c in "${RKB_CHROME:-}" "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome" "/Applications/Chromium.app/Contents/MacOS/Chromium"; do
  [ -n "$c" ] && [ -x "$c" ] && { CHROME="$c"; break; }
done
if [ -z "$CHROME" ]; then
  for c in google-chrome chromium chromium-browser; do
    CHROME=$(command -v "$c" 2>/dev/null) && break
  done
fi
if [ -n "$CHROME" ]; then
  layer "Chrome" found "$CHROME (rkb renders PDFs with it)"
else
  layer "Chrome" missing "rkb PDFs need Chrome or Chromium (or RKB_CHROME=/path)"
fi

if command -v pdftotext >/dev/null 2>&1 && command -v pdfinfo >/dev/null 2>&1; then
  layer "poppler" found "pdftotext and pdfinfo (rkb page-fill checks)"
else
  layer "poppler" missing "brew install poppler (rkb page-fill checks)"
fi

if [ "$OS" != Darwin ] || [ "$ARCH" != arm64 ]; then
  layer "Tessl engine" unavailable "Metal embeddings need macOS on Apple silicon"
elif [ -n "$TESSL_GO" ] && [ -f "$TESSL_GO/engine/target/release/libgusset.a" ]; then
  layer "Tessl engine" found "$TESSL_GO/engine/target/release/libgusset.a (rebuild vani with -tags tessl; docs/SETUP.md step 6)"
elif ! command -v cargo >/dev/null 2>&1; then
  layer "Tessl engine" missing "install Rust (https://rustup.rs), then make -C $TESSL_GO engine"
else
  layer "Tessl engine" missing "make -C $TESSL_GO engine, then rebuild vani with -tags tessl (docs/SETUP.md step 6)"
fi

SNAPS="${HF_HOME:-$HOME/.cache/huggingface}/hub/models--google--embeddinggemma-2/snapshots"
SNAP=""
for d in "$SNAPS"/*/; do [ -d "$d" ] && SNAP="${d%/}"; done
if [ -n "$SNAP" ]; then
  layer "Embedding model" found "$SNAP (set semantic.snapshot to it)"
else
  layer "Embedding model" missing "download google/embeddinggemma-2 from Hugging Face (docs/SETUP.md step 6)"
fi

if command -v lightpanda >/dev/null 2>&1; then
  layer "lightpanda" found "$(command -v lightpanda) (careers-page crawler for pg boards)"
else
  layer "lightpanda" missing "only for pg boards on JS-rendered sites (docs/PHASE0-lightpanda.md)"
fi

echo
if [ "$CONFIG_OK" = 1 ]; then
  echo "done: $V is built and the config checks out. Next:"
  echo "  $V watchlist add --ats gh --token BOARD --company 'Name' --allow-host"
  echo "  VANI_NO_KEYCHAIN=1 $V run-once --list-only"
  echo "Then docs/SETUP.md from step 4 (key, knowledge base, embeddings, DevPrism)."
  exit 0
fi
echo "bootstrap: $V is built, but vani config check failed (output in step 5); fix the config and rerun it" >&2
exit 1
