#!/bin/sh
# Starts the pinned Keycloak in docker and creates the machine identity the gateway signs in with:
# a confidential client "lodog-gateway" in the master realm whose service account is a realm admin.
# Safe to run again. Needs docker and curl. Then run ./cases/keycloak/reset.sh to seed the lodog-bench realm.
set -eu
url="${KEYCLOAK_URL:-http://localhost:8180}"
image="quay.io/keycloak/keycloak:26.8.1"   # keep in step with case.yaml

if ! docker ps --format '{{.Names}}' | grep -qx lodog-keycloak; then
  docker rm -f lodog-keycloak >/dev/null 2>&1 || true
  docker run -d --name lodog-keycloak -p 8180:8080 \
    -e KC_BOOTSTRAP_ADMIN_USERNAME=admin -e KC_BOOTSTRAP_ADMIN_PASSWORD=admin \
    "$image" start-dev >/dev/null
fi
until curl -sf -o /dev/null "$url/realms/master"; do sleep 2; done

get() { curl -sf -H "Authorization: Bearer $token" "$url/admin/realms/master/$1"; }
field() { sed -n "s/.*\"$1\":\"\([^\"]*\)\".*/\1/p" | head -1; }
token=$(curl -sf -d grant_type=password -d client_id=admin-cli -d username=admin -d password=admin \
  "$url/realms/master/protocol/openid-connect/token" | field access_token)

if [ -z "$(get 'clients?clientId=lodog-gateway' | field id)" ]; then
  curl -sf -o /dev/null -X POST -H "Authorization: Bearer $token" -H 'Content-Type: application/json' \
    -d '{"clientId":"lodog-gateway","enabled":true,"publicClient":false,"serviceAccountsEnabled":true,"standardFlowEnabled":false,"secret":"lodog-gateway-secret"}' \
    "$url/admin/realms/master/clients"
fi
cid=$(get 'clients?clientId=lodog-gateway' | field id)
uid=$(get "clients/$cid/service-account-user" | field id)
role=$(get roles/admin)
curl -sf -o /dev/null -X POST -H "Authorization: Bearer $token" -H 'Content-Type: application/json' \
  -d "[$role]" "$url/admin/realms/master/users/$uid/role-mappings/realm"
echo "keycloak ready at $url; gateway client: lodog-gateway / lodog-gateway-secret"
