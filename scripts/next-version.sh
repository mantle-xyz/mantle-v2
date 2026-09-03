#!/usr/bin/env bash
#
# Compute the next unified Mantle version for a component, per VERSION.md §4.3.
#
#   scripts/next-version.sh <repo-url-or-path> <MAJOR.MINOR>
#
#   scripts/next-version.sh https://github.com/mantle-xyz/op-geth 1.6   -> mantle-v1.6.2
#   scripts/next-version.sh ../op-succinct 1.6                         -> mantle-v1.6.1
#
# Prints the tag to cut on stdout and the basis for it on stderr.
# Exits 1 if the computed tag already exists — a collision means the assumed
# state is wrong, so stop and escalate rather than picking the next free number.
#
# MAJOR.MINOR is an input, never inferred: only the version owners open a round
# (VERSION.md §4.1, §5.3).

set -euo pipefail

repo=${1:-}
mm=${2:-}
if [ -z "$repo" ] || [ -z "$mm" ]; then
  echo "usage: $0 <repo-url-or-path> <MAJOR.MINOR>" >&2
  exit 2
fi
case $mm in
  [0-9]*.[0-9]*) ;;
  *) echo "error: MAJOR.MINOR must look like 1.6, got '$mm'" >&2; exit 2 ;;
esac

# Tags must come from the repository, never from VERSION.md: rc tags are not
# recorded there, and using the file alone would reuse a consumed PATCH.
if [ -e "$repo/.git" ]; then
  all=$(git -C "$repo" tag -l)
else
  all=$(git ls-remote --tags "$repo" | sed 's#.*refs/tags/##' | grep -v '\^{}$')
fi

mm_re=$(printf '%s' "$mm" | sed 's/\./\\./g')

# Highest PATCH already consumed under a given tag prefix. An -rc.N or -hotfix
# suffix still counts: a candidate consumes its PATCH even if it never shipped.
highest() {
  printf '%s\n' "$all" \
    | sed -n "s/^$1$mm_re\.\([0-9]\{1,\}\).*/\1/p" \
    | sort -n | tail -1
}

unified=$(highest 'mantle-v')
if [ -n "$unified" ]; then
  patch=$((unified + 1))
  basis="unified line: highest consumed PATCH is .$unified"
else
  legacy=$(highest 'v')
  if [ -n "$legacy" ]; then
    patch=$((legacy + 1))
    basis="first adoption: legacy line's highest consumed PATCH is .$legacy"
  else
    patch=0
    basis="first adoption: no release history on the $mm line"
  fi
fi

version="mantle-v$mm.$patch"

if printf '%s\n' "$all" | grep -qx -- "$version"; then
  echo "error: $version already exists — stop and escalate (VERSION.md §5.8)" >&2
  exit 1
fi

echo "basis: $basis" >&2
echo "$version"
