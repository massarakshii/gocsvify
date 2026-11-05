#!/usr/bin/env bash
# Collects the license texts of all third-party modules that are linked into the binary.
#
# Usage: GOOS=linux GOARCH=amd64 scripts/collect-licenses.sh OUTPUT_FILE
#
# GOOS/GOARCH matter: the set of linked packages (for example golang.org/x/sys) differs per
# platform. Run `go mod download` before the script. The script fails if a module has no
# license file, so a missing license never goes unnoticed.
set -euo pipefail

out="${1:?usage: collect-licenses.sh OUTPUT_FILE}"
: > "$out"

modules="$(go list -deps -f '{{with .Module}}{{if not .Main}}{{.Path}}|{{.Version}}|{{.Dir}}{{end}}{{end}}' ./... | sort -u)"

{
  echo "THIRD-PARTY NOTICES"
  echo "==================="
  echo
  echo "This software includes the following third-party components."
  echo
} >> "$out"

missing=0
while IFS='|' read -r path version dir; do
  [ -n "$path" ] || continue
  files="$(find "$dir" -maxdepth 1 -type f \( -iname 'LICENSE*' -o -iname 'LICENCE*' -o -iname 'COPYING*' -o -iname 'NOTICE*' -o -iname 'PATENTS*' \) | sort)"
  if [ -z "$files" ]; then
    echo "ERROR: no license file found for $path@$version ($dir)" >&2
    missing=1
    continue
  fi
  {
    echo "-------------------------------------------------------------------------------"
    echo "$path $version"
    echo "-------------------------------------------------------------------------------"
    echo
  } >> "$out"
  while IFS= read -r f; do
    cat "$f" >> "$out"
    printf '\n\n' >> "$out"
  done <<< "$files"
done <<< "$modules"

# The Go standard library is linked into every binary; its license is BSD-3-Clause.
goroot="$(go env GOROOT)"
if [ -f "$goroot/LICENSE" ]; then
  {
    echo "-------------------------------------------------------------------------------"
    echo "Go standard library"
    echo "-------------------------------------------------------------------------------"
    echo
    cat "$goroot/LICENSE"
    echo
  } >> "$out"
fi

exit "$missing"
