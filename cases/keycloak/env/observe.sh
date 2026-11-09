#!/bin/sh
# Prints what the running Keycloak actually is, for case.yaml's `versions`.
#   observe.sh image    the image the lodog-keycloak container was started from
#   observe.sh version  the server version Keycloak itself reports (admin serverinfo)
set -eu
url="${KEYCLOAK_URL:-http://localhost:8180}"
case "${1:-}" in
  image) docker inspect lodog-keycloak --format '{{.Config.Image}}' ;;
  version)
    token=$(curl -sf -d grant_type=password -d client_id=admin-cli \
      -d "username=${KEYCLOAK_ADMIN:-admin}" -d "password=${KEYCLOAK_ADMIN_PASSWORD:-admin}" \
      "$url/realms/master/protocol/openid-connect/token" | sed -n 's/.*"access_token":"\([^"]*\)".*/\1/p')
    curl -sf -H "Authorization: Bearer $token" "$url/admin/serverinfo" \
      | sed -n 's/.*"systemInfo":{"version":"\([^"]*\)".*/\1/p' ;;
  *) echo "usage: observe.sh image|version" >&2; exit 2 ;;
esac
