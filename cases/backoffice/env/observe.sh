#!/usr/bin/env sh
set -eu
case "${1:-version}" in
  version) printf '%s\n' anonymized-v1 ;;
  spec) shasum -a 256 cases/backoffice/spec/openapi.json | cut -d ' ' -f 1 ;;
  *) exit 1 ;;
esac
