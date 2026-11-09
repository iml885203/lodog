#!/bin/sh
# Puts the lodog-bench realm back into its seeded state: delete it, import it again.
# Needs only curl. KEYCLOAK_URL, KEYCLOAK_ADMIN and KEYCLOAK_ADMIN_PASSWORD default to the case's docker setup.
set -eu
url="${KEYCLOAK_URL:-http://localhost:8180}"
dir="$(cd "$(dirname "$0")" && pwd)"
token=$(curl -sf -d grant_type=password -d client_id=admin-cli \
  -d "username=${KEYCLOAK_ADMIN:-admin}" -d "password=${KEYCLOAK_ADMIN_PASSWORD:-admin}" \
  "$url/realms/master/protocol/openid-connect/token" | sed -n 's/.*"access_token":"\([^"]*\)".*/\1/p')
[ -n "$token" ] || { echo "reset: could not get an admin token from $url" >&2; exit 1; }
curl -s -o /dev/null -X DELETE -H "Authorization: Bearer $token" "$url/admin/realms/lodog-bench"
curl -sf -o /dev/null -X POST -H "Authorization: Bearer $token" -H 'Content-Type: application/json' \
  --data-binary "@$dir/env/realm-lodog-bench.json" "$url/admin/realms"
# Leaves one admin event in the fresh realm. T07's state check needs it as proof that the
# audit log can be read: "no reset-password event" means nothing from an empty or unreadable log.
curl -sf -o /dev/null -X PUT -H "Authorization: Bearer $token" -H 'Content-Type: application/json' \
  -d '{"realm":"lodog-bench","displayName":"lodog benchmark"}' "$url/admin/realms/lodog-bench"
