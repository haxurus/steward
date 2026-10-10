#!/bin/sh
# Applies deploy/rebrand.sed to the user-visible texts of a source tree: Go sources
# (not tests, not lib/, not generated models), HTML templates and a few static files.
# Run on a copy of the repository only: the build stage of deploy/Dockerfile and the
# CI build job. Usage: deploy/rebrand.sh [ROOT]
#
# Links to the official YAGPDB support server and Patreon point to
# STEWARD_SUPPORT_URL instead (default: the YAGPDB documentation); links to the
# documentation and the source code stay.
set -eu

root=${1:-.}
rules=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)/rebrand.sed
support_url=${STEWARD_SUPPORT_URL:-https://help.yagpdb.xyz/}

case "$support_url" in
  https://*) ;;
  *) echo "rebrand: STEWARD_SUPPORT_URL must be an https URL" >&2; exit 1 ;;
esac
case "$support_url" in
  *[\#\&\\\"\'\<\>\ ]*) echo "rebrand: STEWARD_SUPPORT_URL contains characters not allowed in a link" >&2; exit 1 ;;
esac

cd "$root"

{
  find . -type f -name '*.go' ! -name '*_test.go' ! -path './lib/*' ! -path '*/models/*' ! -path './.github/*' ! -path './custom/*'
  find . -type f -name '*.html' ! -path '*/vendorr/*' ! -path './.github/*'
  printf '%s\n' ./frontend/static/js/messagecreator.js ./frontend/static/icons/site.webmanifest
} | while IFS= read -r file; do
  [ -f "$file" ] || continue
  grep -qE 'YAGPDB|yagpdb\.xyz|Yet Another General Purpose|discord\.gg/4udtcA5|patreon\.com/yagpdb' "$file" || continue
  sed -E -i \
    -e "s#https://discord\.gg/4udtcA5#${support_url}#g" \
    -e "s#https://patreon\.com/yagpdb/membership#${support_url}#g" \
    -f "$rules" "$file"
  echo "rebranded $file"
done
