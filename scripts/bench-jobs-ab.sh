#!/bin/zsh
# Interleaved A/B of `vani jobs list` between a build of an older revision and
# the working tree, over one state directory (docs/BENCHMARKS.md, 2026-11-11).
#
#   scripts/bench-jobs-ab.sh <base-rev> <state-dir> <config.json> [rounds=5]
#
# Run from the repository root. The old binary is built with `go build -overlay`
# over this tree: each non-test .go file changed since <base-rev> is replaced by
# its copy at <base-rev>, and each added one (committed or not) is hidden. A file deleted since then
# stops the script. Each round runs every case with both arms, the arm that goes
# first alternating by round. One line per run: case arm wall_s user_s maxrss_bytes.
# Use a copy of the state: the new arm saves its checkpoint and facts store there.
set -eu
if (( $# < 3 )); then
  echo "usage: $0 <base-rev> <state-dir> <config.json> [rounds]" >&2
  exit 2
fi
base=$1 state=$2 config=$3 n=${4:-5}
work=tmp/bench-jobs-ab
mkdir -p $work
entries=()
for line in ${(f)"$(git diff --name-status $base -- '*.go')"}; do
  st=${line%%$'\t'*}
  p=${line#*$'\t'}
  [[ $p == *_test.go ]] && continue
  case $st in
    M)
      flat=${p//\//_}.txt
      git show "$base:$p" > $work/$flat
      entries+=("\"$PWD/$p\": \"$PWD/$work/$flat\"")
      ;;
    A)
      entries+=("\"$PWD/$p\": \"\"")
      ;;
    *)
      echo "unhandled status $st for $p" >&2
      exit 1
      ;;
  esac
done
# Files not yet committed are new too: hide them from the old build.
for p in ${(f)"$(git ls-files --others --exclude-standard -- '*.go')"}; do
  [[ $p == *_test.go ]] && continue
  entries+=("\"$PWD/$p\": \"\"")
done
print -r -- "{\"Replace\": {${(j:, :)entries}}}" > $work/overlay.json
go build -overlay $work/overlay.json -o $work/vani-old ./cmd/vani
go build -o $work/vani-new ./cmd/vani

export VANI_NO_KEYCHAIN=1 VANI_STATE_DIR=$state VANI_CONFIG=$config
cases=("new|--view new" "allco|--view all --sort company" "salary|--view new --sort salary" "saved|--view saved")
for (( i = 1; i <= n; i++ )); do
  if (( i % 2 )); then arms=(old new); else arms=(new old); fi
  for c in $cases; do
    name=${c%%|*}
    args=(${=c#*|})
    for arm in $arms; do
      /usr/bin/time -l $work/vani-$arm jobs list --json $args > /dev/null 2> $work/time.txt
      wall=$(awk '/ real /{print $1}' $work/time.txt)
      user=$(awk '/ real /{print $3}' $work/time.txt)
      rss=$(awk '/maximum resident set size/{print $1}' $work/time.txt)
      echo "$name $arm $wall $user $rss"
    done
  done
done
