#!/usr/bin/env bash
set -euo pipefail

BASE="http://localhost:8180"
ADMIN_USER="admin"
ADMIN_PASS="admin"
REALM="test"
SIMULATOR_CLIENT="simulator-client"
BACKEND_CLIENT="backend-api"
ROLE_NAME="Backend.Call"

echo "==> Obtaining admin token..."
ADMIN_TOKEN=$(curl -sf \
  -d "client_id=admin-cli" \
  -d "username=${ADMIN_USER}" \
  -d "password=${ADMIN_PASS}" \
  -d "grant_type=password" \
  "${BASE}/realms/master/protocol/openid-connect/token" \
  | jq -r '.access_token')

auth_header="Authorization: Bearer ${ADMIN_TOKEN}"

echo "==> Creating realm '${REALM}'..."
curl -sf -X POST "${BASE}/admin/realms" \
  -H "${auth_header}" \
  -H "Content-Type: application/json" \
  -d "{\"realm\":\"${REALM}\",\"enabled\":true}" \
  || echo "    (realm may already exist, continuing)"

echo "==> Creating client '${BACKEND_CLIENT}'..."
curl -sf -X POST "${BASE}/admin/realms/${REALM}/clients" \
  -H "${auth_header}" \
  -H "Content-Type: application/json" \
  -d "{
    \"clientId\": \"${BACKEND_CLIENT}\",
    \"enabled\": true,
    \"publicClient\": false,
    \"serviceAccountsEnabled\": false,
    \"directAccessGrantsEnabled\": false
  }" || echo "    (client may already exist, continuing)"

echo "==> Getting '${BACKEND_CLIENT}' internal ID..."
BACKEND_CLIENT_UUID=$(curl -sf \
  "${BASE}/admin/realms/${REALM}/clients?clientId=${BACKEND_CLIENT}" \
  -H "${auth_header}" \
  | jq -r '.[0].id')

echo "==> Adding role '${ROLE_NAME}' on '${BACKEND_CLIENT}'..."
curl -sf -X POST "${BASE}/admin/realms/${REALM}/clients/${BACKEND_CLIENT_UUID}/roles" \
  -H "${auth_header}" \
  -H "Content-Type: application/json" \
  -d "{\"name\":\"${ROLE_NAME}\"}" \
  || echo "    (role may already exist, continuing)"

echo "==> Creating client '${SIMULATOR_CLIENT}'..."
curl -sf -X POST "${BASE}/admin/realms/${REALM}/clients" \
  -H "${auth_header}" \
  -H "Content-Type: application/json" \
  -d "{
    \"clientId\": \"${SIMULATOR_CLIENT}\",
    \"enabled\": true,
    \"publicClient\": false,
    \"clientAuthenticatorType\": \"client-secret\",
    \"serviceAccountsEnabled\": true,
    \"directAccessGrantsEnabled\": false
  }" || echo "    (client may already exist, continuing)"

echo "==> Getting '${SIMULATOR_CLIENT}' internal ID..."
SIM_CLIENT_UUID=$(curl -sf \
  "${BASE}/admin/realms/${REALM}/clients?clientId=${SIMULATOR_CLIENT}" \
  -H "${auth_header}" \
  | jq -r '.[0].id')

echo "==> Adding audience mapper to '${SIMULATOR_CLIENT}'..."
curl -sf -X POST "${BASE}/admin/realms/${REALM}/clients/${SIM_CLIENT_UUID}/protocol-mappers/models" \
  -H "${auth_header}" \
  -H "Content-Type: application/json" \
  -d "{
    \"name\": \"audience-backend-api\",
    \"protocol\": \"openid-connect\",
    \"protocolMapper\": \"oidc-audience-mapper\",
    \"consentRequired\": false,
    \"config\": {
      \"included.client.audience\": \"${BACKEND_CLIENT}\",
      \"id.token.claim\": \"false\",
      \"access.token.claim\": \"true\"
    }
  }" || echo "    (mapper may already exist, continuing)"

echo "==> Getting service account user for '${SIMULATOR_CLIENT}'..."
SA_USER_ID=$(curl -sf \
  "${BASE}/admin/realms/${REALM}/clients/${SIM_CLIENT_UUID}/service-account-user" \
  -H "${auth_header}" \
  | jq -r '.id')

echo "==> Getting role '${ROLE_NAME}' definition..."
ROLE_DEF=$(curl -sf \
  "${BASE}/admin/realms/${REALM}/clients/${BACKEND_CLIENT_UUID}/roles/${ROLE_NAME}" \
  -H "${auth_header}")

echo "==> Assigning role '${ROLE_NAME}' to service account..."
curl -sf -X POST \
  "${BASE}/admin/realms/${REALM}/users/${SA_USER_ID}/role-mappings/clients/${BACKEND_CLIENT_UUID}" \
  -H "${auth_header}" \
  -H "Content-Type: application/json" \
  -d "[${ROLE_DEF}]" \
  || echo "    (role may already be assigned, continuing)"

echo "==> Regenerating client secret for '${SIMULATOR_CLIENT}'..."
SECRET_JSON=$(curl -sf -X POST \
  "${BASE}/admin/realms/${REALM}/clients/${SIM_CLIENT_UUID}/client-secret" \
  -H "${auth_header}")
CLIENT_SECRET=$(echo "${SECRET_JSON}" | jq -r '.value')

echo ""
echo "============================================================"
echo "Keycloak setup complete."
echo ""
echo "Copy these values into your .env file:"
echo ""
echo "AZURE_TENANT_ID=test"
echo "AZURE_CLIENT_ID=${SIMULATOR_CLIENT}"
echo "AZURE_CLIENT_SECRET=${CLIENT_SECRET}"
echo "BACKEND_CLIENT_ID=${BACKEND_CLIENT}"
echo "TOKEN_URL=${BASE}/realms/${REALM}/protocol/openid-connect/token"
echo "BACKEND_URL=http://localhost:8080"
echo "FIREBASE_AUTH_EMULATOR_HOST=localhost:9099"
echo "FIREBASE_API_KEY=local-fake-key"
echo "============================================================"
