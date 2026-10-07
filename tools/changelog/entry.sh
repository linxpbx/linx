#!/usr/bin/env bash
# Writes the website's changelog entry for one release (ADR-084, owner
# 2026-10-07): site/src/content/changelog/<version>.md, which linxpbx.com
# shows at /changelog. The Release workflow runs it after a release is
# published and commits the file to master, which redeploys the site.
#
#   tools/changelog/entry.sh v1.2.0 [out-dir]
#
# The words come from docs/release-notes/<tag>.md when the release has one
# (a plain-language summary written before tagging: the way to say what a
# release means for the people using it). Otherwise they're the changes
# since the previous release, from the commit subjects, sorted into fixes
# and improvements; commits about the repository itself (the handoff, the
# website, CI, release tooling, docs) are left out.
set -euo pipefail

tag=${1:?usage: entry.sh vX.Y.Z [out-dir]}
out=${2:-site/src/content/changelog}
version=${tag#v}
case "$version" in
  *-*) channel=beta ;;
  *) channel=stable ;;
esac
date=$(git log -1 --format=%cs "$tag")
prev=$(git describe --tags --abbrev=0 --match 'v[0-9]*' "$tag^" 2>/dev/null || true)

mkdir -p "$out"
file="$out/$version.md"
{
  printf -- '---\n'
  printf 'version: "%s"\n' "$version"
  printf 'date: "%s"\n' "$date"
  printf 'channel: %s\n' "$channel"
  printf 'previous: "%s"\n' "${prev#v}"
  printf -- '---\n'
  if [ -f "docs/release-notes/$tag.md" ]; then
    cat "docs/release-notes/$tag.md"
  else
    if [ -n "$prev" ]; then range="$prev..$tag"; else range="$tag"; fi
    # Subjects only, oldest first, without merges or repository housekeeping.
    subjects=$(git log --no-merges --reverse --format=%s "$range" |
      grep -Eiv '^(handoff|site|ci|release|docs?|readme|changelog|deps|dependabot|build\(deps\)|bump )([:( ]|$)' |
      sed -E 's/ \((ADR|docs\/|internal\/|web\/|ios\/)[^)]*\)//g' || true)
    fixes=$(printf '%s\n' "$subjects" | grep -Ei '(^|[^a-z])(fix|fixes|fixed|bug)([^a-z]|$)' || true)
    other=$(printf '%s\n' "$subjects" | grep -Eiv '(^|[^a-z])(fix|fixes|fixed|bug)([^a-z]|$)' || true)
    if [ -z "$subjects" ]; then
      printf '\nMaintenance: no changes you will notice.\n'
    fi
    if [ -n "$other" ]; then
      printf '\n## Improvements\n\n'
      printf '%s\n' "$other" | sed '/^$/d; s/^/- /'
    fi
    if [ -n "$fixes" ]; then
      printf '\n## Fixes\n\n'
      printf '%s\n' "$fixes" | sed '/^$/d; s/^/- /'
    fi
  fi
} > "$file"
echo "$file"
