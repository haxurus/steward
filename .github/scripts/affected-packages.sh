#!/usr/bin/env bash
# Prints the Go packages whose tests must run for the change BASE..HEAD, one per line:
# every package that contains a changed file (embedded assets included) plus every
# package whose test binary is built from one of them.
#
# Prints "./..." when the whole module must be tested: unknown BASE, module files or
# the test tooling changed. Prints nothing when no Go package is affected.
#
# Usage: affected-packages.sh [BASE]   (BASE: commit SHA, branch or origin/<branch>)
set -euo pipefail

base=${1:-}

resolve() {
  if git cat-file -e "$1^{commit}" 2>/dev/null; then
    printf '%s\n' "$1"
  elif git cat-file -e "origin/$1^{commit}" 2>/dev/null; then
    printf 'origin/%s\n' "$1"
  else
    return 1
  fi
}

if [ -z "$base" ] || [[ "$base" =~ ^0+$ ]] || ! base=$(resolve "$base"); then
  echo "./..."
  exit 0
fi

changed=$(git diff --name-only "$base" HEAD)
[ -n "$changed" ] || exit 0

while IFS= read -r file; do
  case "$file" in
    go.mod | go.sum | .github/workflows/ci.yml | .github/scripts/*)
      echo "./..."
      exit 0
      ;;
  esac
done <<< "$changed"

root=$(pwd)
declare -A pkg_of_dir
while IFS=$'\t' read -r dir path; do
  rel=${dir#"$root"}
  rel=${rel#/}
  pkg_of_dir["${rel:-.}"]=$path
done < <(go list -e -f '{{.Dir}}{{"\t"}}{{.ImportPath}}' ./...)

# A changed file belongs to the nearest enclosing package: this covers templates and
# other files embedded with //go:embed from subdirectories.
declare -A touched
while IFS= read -r file; do
  dir=$(dirname "$file")
  while :; do
    if [ -n "${pkg_of_dir[$dir]+set}" ]; then
      touched["${pkg_of_dir[$dir]}"]=1
      break
    fi
    [ "$dir" = "." ] && break
    dir=$(dirname "$dir")
  done
done <<< "$changed"

[ "${#touched[@]}" -gt 0 ] || exit 0

touched_json=$(jq -cn '$ARGS.positional' --args "${!touched[@]}")

# "p.test" is the test binary of p: its Deps hold every package it is built from,
# test-only imports included ("q [p.test]" names a variant of q recompiled for it).
go list -e -test -json=ImportPath,Deps ./... |
  jq -r --argjson touched "$touched_json" '
    select(.ImportPath | endswith(".test"))
    | (.ImportPath | rtrimstr(".test")) as $pkg
    | select(any(([$pkg] + ((.Deps // []) | map(sub(" \\[.*\\]$"; ""))))[];
        . as $dep | $touched | index($dep) != null))
    | $pkg' |
  sort -u
