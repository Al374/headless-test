#Requires -Version 7.0
Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

$BASE = "http://localhost:8180"
$ADMIN_USER = "admin"
$ADMIN_PASS = "admin"
$REALM = "test"
$SIMULATOR_CLIENT = "simulator-client"
$BACKEND_CLIENT = "backend-api"
$ROLE_NAME = "Backend.Call"

Write-Host "==> Obtaining admin token..."
$tokenBody = @{
    client_id  = "admin-cli"
    username   = $ADMIN_USER
    password   = $ADMIN_PASS
    grant_type = "password"
}
$tokenResp = Invoke-RestMethod -Uri "$BASE/realms/master/protocol/openid-connect/token" `
    -Method Post -Body $tokenBody
$ADMIN_TOKEN = $tokenResp.access_token
$authHeader = @{ Authorization = "Bearer $ADMIN_TOKEN" }

Write-Host "==> Creating realm '$REALM'..."
try {
    Invoke-RestMethod -Uri "$BASE/admin/realms" -Method Post `
        -Headers $authHeader -ContentType "application/json" `
        -Body (ConvertTo-Json @{ realm = $REALM; enabled = $true })
} catch { Write-Host "    (realm may already exist, continuing)" }

Write-Host "==> Creating client '$BACKEND_CLIENT'..."
try {
    Invoke-RestMethod -Uri "$BASE/admin/realms/$REALM/clients" -Method Post `
        -Headers $authHeader -ContentType "application/json" `
        -Body (ConvertTo-Json @{
            clientId                 = $BACKEND_CLIENT
            enabled                  = $true
            publicClient             = $false
            serviceAccountsEnabled   = $false
            directAccessGrantsEnabled = $false
        })
} catch { Write-Host "    (client may already exist, continuing)" }

Write-Host "==> Getting '$BACKEND_CLIENT' internal ID..."
$backendClients = Invoke-RestMethod -Uri "$BASE/admin/realms/$REALM/clients?clientId=$BACKEND_CLIENT" `
    -Headers $authHeader
$BACKEND_CLIENT_UUID = $backendClients[0].id

Write-Host "==> Adding role '$ROLE_NAME' on '$BACKEND_CLIENT'..."
try {
    Invoke-RestMethod -Uri "$BASE/admin/realms/$REALM/clients/$BACKEND_CLIENT_UUID/roles" `
        -Method Post -Headers $authHeader -ContentType "application/json" `
        -Body (ConvertTo-Json @{ name = $ROLE_NAME })
} catch { Write-Host "    (role may already exist, continuing)" }

Write-Host "==> Creating client '$SIMULATOR_CLIENT'..."
try {
    Invoke-RestMethod -Uri "$BASE/admin/realms/$REALM/clients" -Method Post `
        -Headers $authHeader -ContentType "application/json" `
        -Body (ConvertTo-Json @{
            clientId                  = $SIMULATOR_CLIENT
            enabled                   = $true
            publicClient              = $false
            clientAuthenticatorType   = "client-secret"
            serviceAccountsEnabled    = $true
            directAccessGrantsEnabled = $false
        })
} catch { Write-Host "    (client may already exist, continuing)" }

Write-Host "==> Getting '$SIMULATOR_CLIENT' internal ID..."
$simClients = Invoke-RestMethod -Uri "$BASE/admin/realms/$REALM/clients?clientId=$SIMULATOR_CLIENT" `
    -Headers $authHeader
$SIM_CLIENT_UUID = $simClients[0].id

Write-Host "==> Adding audience mapper to '$SIMULATOR_CLIENT'..."
try {
    Invoke-RestMethod -Uri "$BASE/admin/realms/$REALM/clients/$SIM_CLIENT_UUID/protocol-mappers/models" `
        -Method Post -Headers $authHeader -ContentType "application/json" `
        -Body (ConvertTo-Json @{
            name             = "audience-backend-api"
            protocol         = "openid-connect"
            protocolMapper   = "oidc-audience-mapper"
            consentRequired  = $false
            config           = @{
                "included.client.audience" = $BACKEND_CLIENT
                "id.token.claim"           = "false"
                "access.token.claim"       = "true"
            }
        } -Depth 5)
} catch { Write-Host "    (mapper may already exist, continuing)" }

Write-Host "==> Getting service account user for '$SIMULATOR_CLIENT'..."
$saUser = Invoke-RestMethod -Uri "$BASE/admin/realms/$REALM/clients/$SIM_CLIENT_UUID/service-account-user" `
    -Headers $authHeader
$SA_USER_ID = $saUser.id

Write-Host "==> Getting role '$ROLE_NAME' definition..."
$roleDef = Invoke-RestMethod `
    -Uri "$BASE/admin/realms/$REALM/clients/$BACKEND_CLIENT_UUID/roles/$ROLE_NAME" `
    -Headers $authHeader

Write-Host "==> Assigning role '$ROLE_NAME' to service account..."
try {
    Invoke-RestMethod `
        -Uri "$BASE/admin/realms/$REALM/users/$SA_USER_ID/role-mappings/clients/$BACKEND_CLIENT_UUID" `
        -Method Post -Headers $authHeader -ContentType "application/json" `
        -Body (ConvertTo-Json @($roleDef) -Depth 5)
} catch { Write-Host "    (role may already be assigned, continuing)" }

Write-Host "==> Regenerating client secret for '$SIMULATOR_CLIENT'..."
$secretResp = Invoke-RestMethod `
    -Uri "$BASE/admin/realms/$REALM/clients/$SIM_CLIENT_UUID/client-secret" `
    -Method Post -Headers $authHeader
$CLIENT_SECRET = $secretResp.value

Write-Host ""
Write-Host "============================================================"
Write-Host "Keycloak setup complete."
Write-Host ""
Write-Host "Copy these values into your .env file:"
Write-Host ""
Write-Host "AZURE_TENANT_ID=test"
Write-Host "AZURE_CLIENT_ID=$SIMULATOR_CLIENT"
Write-Host "AZURE_CLIENT_SECRET=$CLIENT_SECRET"
Write-Host "BACKEND_CLIENT_ID=$BACKEND_CLIENT"
Write-Host "TOKEN_URL=$BASE/realms/$REALM/protocol/openid-connect/token"
Write-Host "BACKEND_URL=http://localhost:8080"
Write-Host "FIREBASE_AUTH_EMULATOR_HOST=localhost:9099"
Write-Host "FIREBASE_API_KEY=local-fake-key"
Write-Host "============================================================"
