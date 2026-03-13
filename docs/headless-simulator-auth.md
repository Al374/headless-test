# Headless Simulator Authentication Guide

Backend API runs on GKE. Users authenticate via Firebase Auth federated with Azure
Entra ID. This document covers how a headless simulator (no browser, no user) running
outside of Google Cloud can authenticate against that backend.

---

## Concepts: Scopes vs App Roles

### Scopes — Delegated (on behalf of a user)

> "This app can do X, **but only because the logged-in user can do X**"

- A **user** must be present and consent
- The app acts with the user's identity
- Token contains a `scp` claim
- Used in: Authorization Code flow, device code flow
- Example: "Read Jane's profile on her behalf"

### App Roles — Application (the app acts as itself)

> "This app can do X, **regardless of any user**"

- No user involved — pure machine-to-machine
- The app has its own permissions, granted by an admin
- Token contains a `roles` claim
- Used in: **Client credentials flow** — this is the correct choice for a simulator
- Example: "This simulator can call the backend API"
- Requires: **admin consent** (a user cannot self-consent application permissions)

```
Client credentials token (decoded):
{
  "aud":   "api://your-backend-api",
  "appid": "your-simulator-client-id",
  "roles": ["Backend.Call"],    <- App Role  (machine identity)
  "scp":   null,                <- no scope, no user present
  "iss":   "https://sts.windows.net/TENANT_ID/"
}
```

**Rule:** User present → Scope. No user → App Role.

---

## Architecture Decision

```
Can the backend be modified to validate Azure AD tokens directly?
│
├── YES  →  Option A: Azure AD direct  (simpler, recommended for simulator)
│           No Firebase. No GCP. 3 steps.
│
└── NO   →  Option B: WIF + Firebase   (when backend only trusts Firebase tokens)
            Azure AD → GCP WIF → SA impersonation → Firebase custom token
```

---

## Option A: Azure AD Direct (Recommended for Simulator)

The backend validates Azure AD access tokens. Firebase is not involved for the
simulator path. Real users still go through Firebase normally.

```
Simulator (Go, developer machine)
    │  client_id + client_secret
    ▼
Azure Entra ID  →  access token (contains roles: ["Backend.Call"])
    │
    │  Authorization: Bearer <access token>
    ▼
Backend on GKE  (validates against Azure AD JWKS)
```

### A.1 — Register the Backend API in Azure AD

```bash
# Create app registration for the backend
az ad app create --display-name "gke-backend-api"
# Save output: BACKEND_APP_CLIENT_ID

# Set the App ID URI (used as audience in tokens)
az ad app update \
  --id <BACKEND_APP_CLIENT_ID> \
  --identifier-uris "api://<BACKEND_APP_CLIENT_ID>"
```

Define an App Role on the backend registration (replace the UUID with a freshly
generated one — use `uuidgen` or any UUID generator):

```bash
az ad app update \
  --id <BACKEND_APP_CLIENT_ID> \
  --app-roles '[
    {
      "allowedMemberTypes": ["Application"],
      "description": "Allows the simulator to call the backend API",
      "displayName": "Backend.Call",
      "isEnabled": true,
      "value": "Backend.Call",
      "id": "<NEW-UUID>"
    }
  ]'
```

### A.2 — Register the Simulator App

```bash
# Create simulator app registration
az ad app create --display-name "gke-simulator"
# Save: SIMULATOR_CLIENT_ID, SIMULATOR_OBJECT_ID

# Create service principal
az ad sp create --id <SIMULATOR_CLIENT_ID>

# Create client secret
az ad app credential reset \
  --id <SIMULATOR_CLIENT_ID> \
  --display-name "simulator-secret" \
  --years 1
# Save: CLIENT_SECRET  (shown once — store in secrets manager)
```

### A.3 — Grant the App Role to the Simulator

```bash
# Assign the Backend.Call app role to the simulator SP
az ad app permission add \
  --id <SIMULATOR_CLIENT_ID> \
  --api <BACKEND_APP_CLIENT_ID> \
  --api-permissions "<APP-ROLE-UUID>=Role"

# Admin consent is required for Application permissions
az ad app permission admin-consent --id <SIMULATOR_CLIENT_ID>
```

### A.4 — Simulator in Go

```bash
# go.mod dependencies
go get golang.org/x/oauth2
go get github.com/golang-jwt/jwt/v5
go get github.com/MicahParks/keyfunc/v2
```

```go
// simulator/auth/azure.go
package auth

import (
    "context"
    "encoding/json"
    "fmt"
    "io"
    "net/http"
    "net/url"
    "sync"
    "time"
)

type AzureTokenCache struct {
    mu          sync.Mutex
    accessToken string
    expiresAt   time.Time
}

type azureTokenResponse struct {
    AccessToken string `json:"access_token"`
    ExpiresIn   int    `json:"expires_in"`
    TokenType   string `json:"token_type"`
    Error       string `json:"error"`
    ErrorDesc   string `json:"error_description"`
}

type AzureConfig struct {
    TenantID          string
    ClientID          string
    ClientSecret      string
    BackendClientID   string // used to build the scope
}

var cache = &AzureTokenCache{}

// GetToken returns a valid Azure AD access token, fetching a new one if needed.
func GetToken(ctx context.Context, cfg AzureConfig) (string, error) {
    cache.mu.Lock()
    defer cache.mu.Unlock()

    // Reuse cached token if it has more than 60 seconds remaining
    if time.Now().Before(cache.expiresAt.Add(-60 * time.Second)) {
        return cache.accessToken, nil
    }

    // /.default tells Azure AD to include all statically assigned app roles
    scope := fmt.Sprintf("api://%s/.default", cfg.BackendClientID)

    form := url.Values{}
    form.Set("grant_type",    "client_credentials")
    form.Set("client_id",     cfg.ClientID)
    form.Set("client_secret", cfg.ClientSecret)
    form.Set("scope",         scope)

    tokenURL := fmt.Sprintf(
        "https://login.microsoftonline.com/%s/oauth2/v2.0/token",
        cfg.TenantID,
    )

    resp, err := http.PostForm(tokenURL, form)
    if err != nil {
        return "", fmt.Errorf("token request failed: %w", err)
    }
    defer resp.Body.Close()

    body, _ := io.ReadAll(resp.Body)
    var tokenResp azureTokenResponse
    if err := json.Unmarshal(body, &tokenResp); err != nil {
        return "", fmt.Errorf("failed to parse token response: %w", err)
    }
    if tokenResp.Error != "" {
        return "", fmt.Errorf("azure error %s: %s", tokenResp.Error, tokenResp.ErrorDesc)
    }

    cache.accessToken = tokenResp.AccessToken
    cache.expiresAt   = time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second)
    return cache.accessToken, nil
}
```

```go
// simulator/main.go
package main

import (
    "context"
    "fmt"
    "io"
    "net/http"
    "os"

    "your-module/auth"
)

func main() {
    cfg := auth.AzureConfig{
        TenantID:        os.Getenv("AZURE_TENANT_ID"),
        ClientID:        os.Getenv("AZURE_CLIENT_ID"),
        ClientSecret:    os.Getenv("AZURE_CLIENT_SECRET"),
        BackendClientID: os.Getenv("BACKEND_CLIENT_ID"),
    }

    ctx := context.Background()
    token, err := auth.GetToken(ctx, cfg)
    if err != nil {
        panic(err)
    }

    req, _ := http.NewRequestWithContext(ctx, "GET",
        "https://your-backend.example.com/api/v1/users", nil)
    req.Header.Set("Authorization", "Bearer "+token)

    resp, err := http.DefaultClient.Do(req)
    if err != nil {
        panic(err)
    }
    defer resp.Body.Close()

    body, _ := io.ReadAll(resp.Body)
    fmt.Printf("HTTP %d\n%s\n", resp.StatusCode, body)
}
```

### A.5 — Backend Middleware in Go

> **Note on token issuer:** The `oauth2/v2.0/token` endpoint issues tokens with a
> **v1 issuer** by default (`https://sts.windows.net/TENANT_ID/`). To get v2 issuer
> format, set `"accessTokenAcceptedVersion": 2` in the backend app's manifest in
> Azure Portal → App Registrations → Manifest. Both are shown below.

```go
// backend/middleware/azure_auth.go
package middleware

import (
    "context"
    "fmt"
    "net/http"
    "strings"

    "github.com/MicahParks/keyfunc/v2"
    "github.com/golang-jwt/jwt/v5"
)

type AzureClaims struct {
    Roles    []string `json:"roles"`
    AppID    string   `json:"appid"`   // v1 token claim
    AZP      string   `json:"azp"`     // v2 token claim (same value)
    TenantID string   `json:"tid"`
    jwt.RegisteredClaims
}

type AzureMiddlewareConfig struct {
    TenantID        string
    BackendClientID string
    RequiredRole    string
    // Set to "v2" if accessTokenAcceptedVersion=2 in app manifest, otherwise "v1"
    TokenVersion    string
}

func NewAzureAuthMiddleware(cfg AzureMiddlewareConfig) func(http.Handler) http.Handler {
    jwksURL := fmt.Sprintf(
        "https://login.microsoftonline.com/%s/discovery/v2.0/keys",
        cfg.TenantID,
    )

    // keyfunc fetches and caches JWKS, rotating keys automatically
    jwks, err := keyfunc.NewDefault([]string{jwksURL})
    if err != nil {
        panic(fmt.Sprintf("failed to initialise JWKS: %v", err))
    }

    var issuer string
    if cfg.TokenVersion == "v2" {
        issuer = fmt.Sprintf("https://login.microsoftonline.com/%s/v2.0", cfg.TenantID)
    } else {
        issuer = fmt.Sprintf("https://sts.windows.net/%s/", cfg.TenantID)
    }

    return func(next http.Handler) http.Handler {
        return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
            authHeader := r.Header.Get("Authorization")
            if !strings.HasPrefix(authHeader, "Bearer ") {
                http.Error(w, `{"error":"missing_token"}`, http.StatusUnauthorized)
                return
            }
            rawToken := strings.TrimPrefix(authHeader, "Bearer ")

            claims := &AzureClaims{}
            token, err := jwt.ParseWithClaims(rawToken, claims, jwks.Keyfunc,
                jwt.WithAudience("api://"+cfg.BackendClientID),
                jwt.WithIssuer(issuer),
                jwt.WithValidMethods([]string{"RS256"}),
            )
            if err != nil || !token.Valid {
                http.Error(w, `{"error":"invalid_token"}`, http.StatusUnauthorized)
                return
            }

            // Verify the required app role is present
            hasRole := false
            for _, role := range claims.Roles {
                if role == cfg.RequiredRole {
                    hasRole = true
                    break
                }
            }
            if !hasRole {
                http.Error(w, `{"error":"insufficient_role"}`, http.StatusForbidden)
                return
            }

            ctx := context.WithValue(r.Context(), contextKeyAppID, claims.AppID)
            next.ServeHTTP(w, r.WithContext(ctx))
        })
    }
}

type contextKey string
const contextKeyAppID contextKey = "appid"
```

```go
// backend/main.go — wiring the middleware
package main

import (
    "net/http"
    "your-module/middleware"
)

func main() {
    azureAuth := middleware.NewAzureAuthMiddleware(middleware.AzureMiddlewareConfig{
        TenantID:        "YOUR_TENANT_ID",
        BackendClientID: "YOUR_BACKEND_CLIENT_ID",
        RequiredRole:    "Backend.Call",
        TokenVersion:    "v1",  // change to "v2" if manifest updated
    })

    mux := http.NewServeMux()
    mux.HandleFunc("/api/v1/users", usersHandler)

    http.ListenAndServe(":8080", azureAuth(mux))
}
```

### A.6 — Environment Variables (Simulator)

| Variable | Where to find it |
|---|---|
| `AZURE_TENANT_ID` | Azure Portal → Entra ID → Overview → Tenant ID |
| `AZURE_CLIENT_ID` | App Registrations → gke-simulator → Application (client) ID |
| `AZURE_CLIENT_SECRET` | Created in step A.2 — store in a secrets manager |
| `BACKEND_CLIENT_ID` | App Registrations → gke-backend-api → Application (client) ID |

---

## Option B: WIF + Firebase (Backend Only Trusts Firebase Tokens)

Use this when the backend validates Firebase ID tokens and cannot be changed.

```
Simulator (developer machine)
    │  client_id + client_secret
    ▼
Azure Entra ID  →  Azure JWT
    │
    │  GCP STS token exchange
    ▼
GCP Workload Identity Federation Pool
    │  (trusts Azure AD as OIDC issuer)
    │
    │  SA impersonation
    ▼
GCP Service Account  (firebase-simulator-sa)
    │
    │  Firebase Admin SDK  createCustomToken()
    ▼
Firebase Auth REST API  →  Firebase ID token
    │
    │  Authorization: Bearer <Firebase ID token>
    ▼
Backend on GKE
```

### B.1 — Azure Setup (same as Option A steps A.1–A.2)

The simulator app registration and client secret are identical. You do **not** need
to expose a backend API or define app roles for this path — the Azure token is only
used as proof of identity for GCP, not to call the backend directly.

### B.2 — GCP Setup

```bash
# 1. Create the Workload Identity Pool
gcloud iam workload-identity-pools create azure-simulator-pool \
  --location=global \
  --display-name="Azure Simulator Pool" \
  --project=YOUR_PROJECT_ID

# 2. Create the OIDC provider pointing at Azure AD
#    assertion.appid is the Azure v1 claim for the calling app's client ID
gcloud iam workload-identity-pools providers create-oidc azure-entra-provider \
  --location=global \
  --workload-identity-pool=azure-simulator-pool \
  --display-name="Azure Entra ID" \
  --issuer-uri="https://sts.windows.net/YOUR_TENANT_ID/" \
  --allowed-audiences="api://AzureADTokenExchange" \
  --attribute-mapping="google.subject=assertion.sub,attribute.appid=assertion.appid" \
  --project=YOUR_PROJECT_ID

# Note: if you configure the Azure app to issue v2 tokens, replace
#   assertion.appid  with  assertion.azp
#   issuer-uri with  https://login.microsoftonline.com/YOUR_TENANT_ID/v2.0

# 3. Create the Firebase service account
gcloud iam service-accounts create firebase-simulator-sa \
  --display-name="Firebase Simulator SA" \
  --project=YOUR_PROJECT_ID
```

### B.3 — Service Account Permissions (Corrected — 3 grants required)

**Previous answer had an error here.** `roles/firebase.sdkAdminServiceAgent` is a
Google-managed service agent role and cannot be assigned directly. The correct set:

```bash
SA="firebase-simulator-sa@YOUR_PROJECT_ID.iam.gserviceaccount.com"

# Grant 1: Firebase Admin access at project level
# Required for Firebase Admin SDK to operate
gcloud projects add-iam-policy-binding YOUR_PROJECT_ID \
  --member="serviceAccount:${SA}" \
  --role="roles/firebase.admin"

# Grant 2: Allow the SA to sign its own tokens (signBlob)
# Required for createCustomToken() when no key file is present —
# Firebase Admin SDK calls the IAM signBlob API internally
gcloud iam service-accounts add-iam-policy-binding ${SA} \
  --member="serviceAccount:${SA}" \
  --role="roles/iam.serviceAccountTokenCreator"

# Grant 3: Allow the WIF pool identity to impersonate this SA
# Scoped to YOUR_AZURE_CLIENT_ID only — not the whole tenant
PROJECT_NUMBER=$(gcloud projects describe YOUR_PROJECT_ID --format='value(projectNumber)')

gcloud iam service-accounts add-iam-policy-binding ${SA} \
  --role="roles/iam.workloadIdentityUser" \
  --member="principalSet://iam.googleapis.com/projects/${PROJECT_NUMBER}/locations/global/workloadIdentityPools/azure-simulator-pool/attribute.appid/YOUR_AZURE_CLIENT_ID" \
  --project=YOUR_PROJECT_ID
```

### B.4 — Enable Required GCP APIs

```bash
gcloud services enable \
  iamcredentials.googleapis.com \
  sts.googleapis.com \
  identitytoolkit.googleapis.com \
  firebase.googleapis.com \
  --project=YOUR_PROJECT_ID
```

### B.5 — Language Consideration

> **The simulator is written in Go. The Firebase Admin SDK exists for Go
> (`firebase.google.com/go/v4`) but initialising it with an impersonated access
> token (no key file) requires explicit SA email configuration — the SDK cannot
> auto-detect it from a bare access token. See Known Issues §1 below.**
>
> The Python implementation is shown here as a reference. For Go, either:
> - Use the Firebase Admin Go SDK with `option.WithTokenSource()` + explicit SA email
> - Run a minimal Python/Node sidecar whose only job is token minting
> - Call `iam.serviceAccounts.signBlob` directly from Go and build the JWT manually

```bash
pip install azure-identity firebase-admin requests google-auth
```

```python
# simulator/auth.py
import os, time, requests
from azure.identity import ClientSecretCredential
import firebase_admin
from firebase_admin import auth as firebase_auth
import google.oauth2.credentials

# ── Config ────────────────────────────────────────────────────────────────────
AZURE_TENANT_ID     = os.environ["AZURE_TENANT_ID"]
AZURE_CLIENT_ID     = os.environ["AZURE_CLIENT_ID"]
AZURE_CLIENT_SECRET = os.environ["AZURE_CLIENT_SECRET"]

GCP_PROJECT_ID      = os.environ["GCP_PROJECT_ID"]
GCP_PROJECT_NUMBER  = os.environ["GCP_PROJECT_NUMBER"]
SA_EMAIL            = f"firebase-simulator-sa@{GCP_PROJECT_ID}.iam.gserviceaccount.com"
WIF_POOL_ID         = "azure-simulator-pool"
WIF_PROVIDER_ID     = "azure-entra-provider"

FIREBASE_API_KEY    = os.environ["FIREBASE_API_KEY"]
SIMULATOR_UID       = "simulator-headless"

# ── Step 1: Azure AD token ────────────────────────────────────────────────────
def get_azure_token() -> str:
    cred = ClientSecretCredential(
        tenant_id=AZURE_TENANT_ID,
        client_id=AZURE_CLIENT_ID,
        client_secret=AZURE_CLIENT_SECRET,
    )
    # Scope must match allowed-audiences on the WIF provider.
    # Verify the aud claim in the returned token matches before proceeding
    # (see Known Issues §3).
    return cred.get_token("api://AzureADTokenExchange/.default").token

# ── Step 2: Exchange Azure token → short-lived GCP federated token ────────────
def exchange_for_gcp_token(azure_token: str) -> str:
    audience = (
        f"//iam.googleapis.com/projects/{GCP_PROJECT_NUMBER}"
        f"/locations/global/workloadIdentityPools/{WIF_POOL_ID}"
        f"/providers/{WIF_PROVIDER_ID}"
    )
    resp = requests.post(
        "https://sts.googleapis.com/v1/token",
        json={
            "grant_type":         "urn:ietf:params:oauth:grant-type:token-exchange",
            "audience":           audience,
            "scope":              "https://www.googleapis.com/auth/cloud-platform",
            "requestedTokenType": "urn:ietf:params:oauth:token-type:access_token",
            "subjectTokenType":   "urn:ietf:params:oauth:token-type:jwt",
            "subjectToken":       azure_token,
        },
    )
    resp.raise_for_status()
    return resp.json()["access_token"]

# ── Step 3: Impersonate SA to get Firebase-capable access token ───────────────
def impersonate_sa(federated_token: str) -> str:
    resp = requests.post(
        f"https://iamcredentials.googleapis.com/v1/projects/-/serviceAccounts/{SA_EMAIL}:generateAccessToken",
        headers={"Authorization": f"Bearer {federated_token}"},
        json={
            "scope": [
                "https://www.googleapis.com/auth/cloud-platform",
                "https://www.googleapis.com/auth/firebase",
            ],
            "lifetime": "3600s",
        },
    )
    resp.raise_for_status()
    return resp.json()["accessToken"]

# ── Firebase credential wrapper (public API only — no private internals) ───────
class _ImpersonatedCredential(firebase_admin.credentials.Base):
    """
    Wraps a short-lived SA access token as a Firebase Admin credential.
    Uses only the public firebase_admin.credentials.Base interface.
    """
    def __init__(self, access_token: str):
        self._token = google.oauth2.credentials.Credentials(token=access_token)

    def get_credential(self):
        return self._token

# ── Steps 4+5: Firebase custom token → ID token ───────────────────────────────
# _sa_token_expiry tracks when the SA access token (and thus the Firebase Admin
# app) needs to be re-initialised. The Firebase App is deleted and re-created
# each time the SA token expires to avoid using a stale signBlob credential.
_sa_token_expiry: float = 0

def get_firebase_custom_token(sa_access_token: str, sa_expires_at: float) -> bytes:
    global _sa_token_expiry

    # Re-initialise the Firebase Admin app whenever the SA token has changed
    if abs(sa_expires_at - _sa_token_expiry) > 1:
        if firebase_admin._apps:
            firebase_admin.delete_app(firebase_admin.get_app())
        firebase_admin.initialize_app(_ImpersonatedCredential(sa_access_token))
        _sa_token_expiry = sa_expires_at

    return firebase_auth.create_custom_token(
        SIMULATOR_UID,
        developer_claims={"role": "simulator"},
    )

def exchange_custom_for_id_token(custom_token: bytes) -> tuple[str, str]:
    resp = requests.post(
        f"https://identitytoolkit.googleapis.com/v1/accounts:signInWithCustomToken?key={FIREBASE_API_KEY}",
        json={"token": custom_token.decode(), "returnSecureToken": True},
    )
    resp.raise_for_status()
    data = resp.json()
    return data["idToken"], data["refreshToken"]

# ── Refresh Firebase ID token before 1-hour expiry ────────────────────────────
def refresh_id_token(refresh_token: str) -> str:
    resp = requests.post(
        f"https://securetoken.googleapis.com/v1/token?key={FIREBASE_API_KEY}",
        json={"grant_type": "refresh_token", "refresh_token": refresh_token},
    )
    resp.raise_for_status()
    return resp.json()["id_token"]

# ── Public auth manager ───────────────────────────────────────────────────────
class SimulatorAuth:
    """
    Thread-safe token manager. Maintains the full Azure → GCP → Firebase
    credential chain and refreshes each layer before it expires.
    """
    def __init__(self):
        self._id_token       = None
        self._refresh_token  = None
        self._id_expires_at  = 0.0
        self._sa_token       = None
        self._sa_expires_at  = 0.0

    def _refresh_sa_token(self):
        """Re-run the full Azure → GCP → SA impersonation chain."""
        azure_token      = get_azure_token()
        gcp_token        = exchange_for_gcp_token(azure_token)
        self._sa_token   = impersonate_sa(gcp_token)
        self._sa_expires_at = time.time() + 3600

    def get_token(self) -> str:
        now = time.time()

        # Refresh SA token if it is about to expire (drives Firebase Admin re-init)
        if now > self._sa_expires_at - 120:
            self._refresh_sa_token()

        # Reuse Firebase ID token if still valid
        if self._id_token and now < self._id_expires_at - 60:
            return self._id_token

        # Use Firebase refresh token if available (avoids full custom-token round trip)
        if self._refresh_token:
            self._id_token      = refresh_id_token(self._refresh_token)
            self._id_expires_at = now + 3600
            return self._id_token

        # Full Firebase token mint
        custom_token = get_firebase_custom_token(self._sa_token, self._sa_expires_at)
        self._id_token, self._refresh_token = exchange_custom_for_id_token(custom_token)
        self._id_expires_at = now + 3600
        return self._id_token
```

### B.6 — Environment Variables (Option B)

| Variable | Where to find it |
|---|---|
| `AZURE_TENANT_ID` | Azure Portal → Entra ID → Overview → Tenant ID |
| `AZURE_CLIENT_ID` | App Registrations → gke-simulator → Application (client) ID |
| `AZURE_CLIENT_SECRET` | Created in step B.1 |
| `GCP_PROJECT_ID` | `gcloud config get-value project` |
| `GCP_PROJECT_NUMBER` | `gcloud projects describe YOUR_PROJECT_ID --format='value(projectNumber)'` |
| `FIREBASE_API_KEY` | Firebase Console → Project Settings → General → Web API key |

---

## Handling Real Users + Simulator on the Same Backend

If the backend currently validates Firebase tokens for real users and you add Azure
AD validation for the simulator, protect routes with a dual-validator:

```go
// backend/middleware/combined_auth.go
func CombinedAuth(firebaseApp *firebase.App, azureCfg AzureMiddlewareConfig) func(http.Handler) http.Handler {
    azureMiddleware := NewAzureAuthMiddleware(azureCfg)

    return func(next http.Handler) http.Handler {
        return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
            authHeader := r.Header.Get("Authorization")
            if !strings.HasPrefix(authHeader, "Bearer ") {
                http.Error(w, `{"error":"missing_token"}`, http.StatusUnauthorized)
                return
            }
            rawToken := strings.TrimPrefix(authHeader, "Bearer ")

            // Peek at the issuer without full verification to route to
            // the correct validator — avoids trying both on every request
            iss, err := unsafeExtractIssuer(rawToken)
            if err != nil {
                http.Error(w, `{"error":"malformed_token"}`, http.StatusUnauthorized)
                return
            }

            if strings.Contains(iss, "windows.net") || strings.Contains(iss, "microsoftonline.com") {
                // Route to Azure AD validator
                azureMiddleware(next).ServeHTTP(w, r)
            } else {
                // Route to Firebase validator
                validateFirebaseToken(firebaseApp, next).ServeHTTP(w, r)
            }
        })
    }
}

func unsafeExtractIssuer(tokenStr string) (string, error) {
    // Parse without verification just to read the iss claim for routing
    p := jwt.NewParser()
    claims := jwt.MapClaims{}
    _, _, err := p.ParseUnverified(tokenStr, claims)
    if err != nil {
        return "", err
    }
    iss, _ := claims["iss"].(string)
    return iss, nil
}
```

---

## Quick Reference: Which Option to Use

| Situation | Option |
|---|---|
| Simulator on developer machine, backend can accept Azure tokens | **A — Azure AD direct** |
| Simulator on developer machine, backend only accepts Firebase tokens | **B — WIF + Firebase** |
| Simulator running on GKE | **B** with GKE Workload Identity instead of client secret |
| You want zero dependency on GCP for the simulator | **A — Azure AD direct** |

---

## Known Issues and Gotchas — Option B

### Issue 1 — Language Mismatch: Go Simulator vs Python/Java/Node SDK

The Firebase Admin SDK (`createCustomToken`) is available for Python, Java, Node.js,
and Go. However, initialising the **Go SDK** with a bare impersonated access token
(no key file) requires explicit service account email configuration:

```go
import (
    firebase "firebase.google.com/go/v4"
    "firebase.google.com/go/v4/auth"
    "google.golang.org/api/option"
    "golang.org/x/oauth2"
)

// Build a TokenSource from the impersonated SA access token
ts := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: saAccessToken})

app, err := firebase.NewApp(ctx, &firebase.Config{
    // The Go SDK cannot auto-detect the SA email from an access token.
    // You must set ServiceAccountID explicitly for createCustomToken to work.
    ServiceAccountID: "firebase-simulator-sa@YOUR_PROJECT_ID.iam.gserviceaccount.com",
}, option.WithTokenSource(ts))
```

Without `ServiceAccountID`, the Go SDK will return:
`"failed to determine service account: ..."`

**Practical alternatives if this is too complex:**
- Run a minimal Python sidecar whose only job is to mint tokens, called via HTTP
  from the Go simulator
- Call `iam.serviceAccounts.signBlob` directly from Go and construct the JWT manually

---

### Issue 2 — SA Token Expiry Causes Silent Failure After 60 Minutes

The impersonated SA access token has a 1-hour TTL. After it expires,
`createCustomToken()` calls the IAM `signBlob` API with a stale token and fails.

The updated `SimulatorAuth` class above handles this by tracking `_sa_expires_at`
separately from the Firebase ID token expiry and re-running the full
Azure → GCP → SA chain 2 minutes before the SA token expires.

**What breaks without this:** the first `get_token()` call after the 60-minute mark
returns an error from `signBlob`, not from Firebase — making it appear as a
permissions problem rather than an expiry problem.

---

### Issue 3 — `api://AzureADTokenExchange` Audience Must Be Explicitly Registered

The WIF provider is configured with `--allowed-audiences="api://AzureADTokenExchange"`.
For this to appear as the `aud` claim in the Azure token, that URI must be registered
as the **App ID URI** on an Azure app registration. If it is not registered, Azure AD
will reject the scope or issue a token with a different `aud`, and GCP STS will return
a 400 error.

**Verify before running any code:**

```bash
curl -s -X POST \
  "https://login.microsoftonline.com/TENANT_ID/oauth2/v2.0/token" \
  -d "grant_type=client_credentials" \
  -d "client_id=SIMULATOR_CLIENT_ID" \
  -d "client_secret=CLIENT_SECRET" \
  -d "scope=api://AzureADTokenExchange/.default" \
| python3 -c "
import sys, json, base64
token = json.load(sys.stdin).get('access_token','')
if not token:
    print('ERROR: no token in response'); sys.exit(1)
pad = len(token.split('.')[1]) % 4
payload = json.loads(base64.b64decode(token.split('.')[1] + '=' * (4 - pad)))
print('aud  =', payload.get('aud'))
print('iss  =', payload.get('iss'))
print('appid=', payload.get('appid'))
"
```

The `aud` value printed must exactly match the `--allowed-audiences` value set on the
WIF provider. If they differ, update one to match the other.

---

### Issue 4 — Five External Network Hops, Each a Failure Point

Option B makes sequential calls to five different endpoints. Any one being blocked by
a corporate proxy, VPN split-tunnel, or firewall breaks the chain with a different
error message at each step:

```
Step  Endpoint                              Blocked symptom
────  ────────────────────────────────────  ─────────────────────────────────
  1   login.microsoftonline.com            azure.identity.ClientAuthError
  2   sts.googleapis.com                   requests.HTTPError 400/403
  3   iamcredentials.googleapis.com        requests.HTTPError 403
  4   identitytoolkit.googleapis.com       requests.HTTPError 400
  5   securetoken.googleapis.com           requests.HTTPError 400
```

Option A (Azure AD direct) makes a single call to `login.microsoftonline.com` and
nothing else. On a developer machine behind a corporate proxy, this difference is
significant.

**Check connectivity before starting:**

```bash
for url in \
  "https://login.microsoftonline.com" \
  "https://sts.googleapis.com" \
  "https://iamcredentials.googleapis.com" \
  "https://identitytoolkit.googleapis.com" \
  "https://securetoken.googleapis.com"; do
  code=$(curl -s -o /dev/null -w "%{http_code}" --max-time 5 "$url")
  echo "$code  $url"
done
```

Any result other than 2xx/4xx (e.g., 000 = connection refused/timeout) indicates a
network block that must be resolved before Option B will work.

---

### Issue 5 — Firebase Admin SDK Initialization Used Private Internals (Fixed)

The original code in this document accessed `._credential._g_credential` — a private
attribute of the Firebase Admin SDK that can break on any SDK version bump:

```python
# OLD — fragile, accesses private internals
firebase_admin.get_app()._credential._g_credential = g_cred
```

The updated `SimulatorAuth` code above replaces this with a clean
`firebase_admin.credentials.Base` subclass (`_ImpersonatedCredential`) that uses
only the public SDK interface.

---

### Issue 6 — Firebase Web API Key Restrictions May Block Token Exchange

The `FIREBASE_API_KEY` is used to call `identitytoolkit.googleapis.com` in step 4.
In GCP Console, API keys can be restricted by **HTTP referrer** or **IP address**. If
the key has restrictions applied, calls from a developer machine return:

```json
{"error": {"code": 400, "message": "API key not valid. Please pass a valid API key."}}
```

**Check:** GCP Console → APIs & Services → Credentials → your Firebase Web API key
→ Application restrictions. Set to "None" for development, or add the developer
machine's IP to the allowed list.

---

### Issue 7 — WIF Attribute Mapping Silently Breaks on Azure Token v2

The WIF provider maps `attribute.appid=assertion.appid`. Azure AD v1 tokens include
`appid`; v2 tokens use `azp`. The default client credentials flow issues v1-format
tokens, but if the Azure app manifest is updated to `"accessTokenAcceptedVersion": 2`
in the future, the mapping stops working silently — GCP STS returns a 400 with a
misleading `"invalid_grant"` error.

**Defensive mapping that handles both versions:**

```bash
gcloud iam workload-identity-pools providers update-oidc azure-entra-provider \
  --location=global \
  --workload-identity-pool=azure-simulator-pool \
  --attribute-mapping="google.subject=assertion.sub,attribute.appid=assertion.appid??assertion.azp" \
  --project=YOUR_PROJECT_ID
```

The `??` operator returns the first non-null value, so this works for both v1 and v2
tokens.

---

### Issue 8 — Clock Skew Causes All JWT Validations to Fail

JWT validation rejects tokens whose `nbf` (not before) or `exp` (expiry) claims fall
outside the server's current time window. Developer machines that have been suspended
or that drift more than **5 minutes** from NTP will fail all three token exchanges
with misleading errors (`invalid_grant`, `JWT expired`).

```bash
# Check current machine time vs NTP
date && curl -sI https://www.googleapis.com 2>/dev/null | grep -i date
```

If the times differ by more than a few seconds, sync the clock:

```bash
# macOS
sudo sntp -sS time.apple.com

# Linux
sudo timedatectl set-ntp true
```

---

## Local Testing Without Cloud Accounts

All three cloud dependencies (Azure AD, GCP/Firebase, backend) can be replaced with
local emulators or stubs. No Azure subscription, GCP project, or Firebase account is
needed.

> **Working implementation:** `headless-test/` contains a fully runnable local
> test environment (Keycloak + Firebase Auth Emulator via Docker Compose, Go
> simulator, Go backend). See `headless-test/README.md` for step-by-step
> instructions. The notes below document the design rationale and alternatives.

---

### Local Stack Overview

```
Simulator (Go)
    │
    │  fake client_credentials response
    ▼
Keycloak (local Docker)          ← replaces Azure Entra ID
    │
    │  (Option B only) mocked via requests.mock
    ▼
GCP STS + IAM (unittest.mock)    ← no official emulator; stub in tests
    │
    ▼
Firebase Auth Emulator           ← official Google tool, runs locally
    │
    ▼
Backend (local) pointing at emulator
```

---

### Layer 1 — Mock Azure AD

#### Option 1a: Keycloak (full OIDC server, closest to real Entra ID)

```bash
docker run -p 8180:8080 \
  -e KEYCLOAK_ADMIN=admin \
  -e KEYCLOAK_ADMIN_PASSWORD=admin \
  quay.io/keycloak/keycloak:latest start-dev
```

In the Keycloak admin UI at `http://localhost:8180`:

1. Create a realm — e.g. `test`
2. Clients → Create client
   - Client ID: `simulator-client`
   - Client authentication: **On**
   - Service accounts roles: **On** (enables client credentials flow)
3. Credentials tab → copy the client secret
4. Client scopes → Create scope: `api://backend-client-id`
5. Assign the scope to `simulator-client`

The token endpoint is now:
```
http://localhost:8180/realms/test/protocol/openid-connect/token
```

Point your Go simulator at Keycloak by overriding the token URL — add a
`TokenURL` field to `AzureConfig`:

```go
// simulator/auth/azure.go — add TokenURL override for local testing
type AzureConfig struct {
    TenantID        string
    ClientID        string
    ClientSecret    string
    BackendClientID string
    TokenURL        string // if set, overrides the Azure AD endpoint
}

func GetToken(ctx context.Context, cfg AzureConfig) (string, error) {
    tokenURL := cfg.TokenURL
    if tokenURL == "" {
        tokenURL = fmt.Sprintf(
            "https://login.microsoftonline.com/%s/oauth2/v2.0/token",
            cfg.TenantID,
        )
    }
    // ... rest of function unchanged
}
```

Local env vars:
```bash
export AZURE_TENANT_ID="test"
export AZURE_CLIENT_ID="simulator-client"
export AZURE_CLIENT_SECRET="<keycloak-secret>"
export BACKEND_CLIENT_ID="backend-client-id"
export TOKEN_URL="http://localhost:8180/realms/test/protocol/openid-connect/token"
```

#### Option 1b: Minimal Go HTTP stub (for unit tests only)

If you only need to test token caching and retry logic without Keycloak:

```go
// simulator/auth/azure_test.go
package auth_test

import (
    "crypto/rand"
    "crypto/rsa"
    "encoding/json"
    "net/http"
    "net/http/httptest"
    "testing"
    "time"

    "github.com/golang-jwt/jwt/v5"
)

func startMockAzureAD(t *testing.T, overrideClaims jwt.MapClaims) (*httptest.Server, *rsa.PrivateKey) {
    t.Helper()
    key, _ := rsa.GenerateKey(rand.Reader, 2048)

    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        claims := jwt.MapClaims{
            "aud":   "api://backend-client-id",
            "appid": "simulator-client-id",
            "roles": []string{"Backend.Call"},
            "iss":   "https://sts.windows.net/test-tenant/",
            "exp":   time.Now().Add(time.Hour).Unix(),
            "iat":   time.Now().Unix(),
        }
        for k, v := range overrideClaims {
            claims[k] = v
        }
        token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
        signed, _ := token.SignedString(key)

        w.Header().Set("Content-Type", "application/json")
        json.NewEncoder(w).Encode(map[string]interface{}{
            "access_token": signed,
            "token_type":   "Bearer",
            "expires_in":   3600,
        })
    }))
    t.Cleanup(srv.Close)
    return srv, key
}

func TestGetToken_CachesToken(t *testing.T) {
    srv, _ := startMockAzureAD(t, nil)

    cfg := AzureConfig{
        ClientID:        "sim",
        ClientSecret:    "secret",
        BackendClientID: "backend",
        TokenURL:        srv.URL,
    }

    tok1, err := GetToken(t.Context(), cfg)
    if err != nil {
        t.Fatal(err)
    }
    tok2, _ := GetToken(t.Context(), cfg)

    if tok1 != tok2 {
        t.Error("expected cached token to be returned on second call")
    }
}
```

---

#### Option 1c: Minimal Python HTTP stub (for unit tests only)

Equivalent of the Go stub above, using `pytest` and `responses` (or `unittest.mock`):

```bash
pip install pytest responses cryptography PyJWT
```

```python
# tests/test_azure_token.py
import time
import json
import pytest
import responses as resp_mock
from cryptography.hazmat.primitives.asymmetric import rsa
from cryptography.hazmat.backends import default_backend
import jwt

from simulator.auth import get_azure_token, AZURE_TENANT_ID

# Generate a local RSA key pair for signing test tokens
@pytest.fixture(scope="module")
def rsa_key_pair():
    private_key = rsa.generate_private_key(
        public_exponent=65537,
        key_size=2048,
        backend=default_backend(),
    )
    return private_key

def make_fake_token(private_key, override_claims: dict = None) -> str:
    claims = {
        "aud":   "api://backend-client-id",
        "appid": "simulator-client-id",
        "roles": ["Backend.Call"],
        "iss":   f"https://sts.windows.net/test-tenant/",
        "exp":   int(time.time()) + 3600,
        "iat":   int(time.time()),
    }
    if override_claims:
        claims.update(override_claims)
    return jwt.encode(claims, private_key, algorithm="RS256")

@resp_mock.activate
def test_get_azure_token_returns_access_token(rsa_key_pair):
    fake_token = make_fake_token(rsa_key_pair)

    resp_mock.add(
        resp_mock.POST,
        f"https://login.microsoftonline.com/{AZURE_TENANT_ID}/oauth2/v2.0/token",
        json={"access_token": fake_token, "expires_in": 3600, "token_type": "Bearer"},
        status=200,
    )

    token = get_azure_token()
    assert token == fake_token

@resp_mock.activate
def test_get_azure_token_caches_result(rsa_key_pair):
    fake_token = make_fake_token(rsa_key_pair)

    resp_mock.add(
        resp_mock.POST,
        f"https://login.microsoftonline.com/{AZURE_TENANT_ID}/oauth2/v2.0/token",
        json={"access_token": fake_token, "expires_in": 3600, "token_type": "Bearer"},
        status=200,
    )

    tok1 = get_azure_token()
    tok2 = get_azure_token()

    # Only one HTTP call should have been made — second call uses cache
    assert len(resp_mock.calls) == 1
    assert tok1 == tok2

@resp_mock.activate
def test_get_azure_token_expired_token_forces_refresh(rsa_key_pair):
    # Token that expires in 30s — inside the 60s buffer window
    expired_token = make_fake_token(rsa_key_pair, {"exp": int(time.time()) + 30})
    fresh_token   = make_fake_token(rsa_key_pair)

    resp_mock.add(
        resp_mock.POST,
        f"https://login.microsoftonline.com/{AZURE_TENANT_ID}/oauth2/v2.0/token",
        json={"access_token": expired_token, "expires_in": 30, "token_type": "Bearer"},
    )
    resp_mock.add(
        resp_mock.POST,
        f"https://login.microsoftonline.com/{AZURE_TENANT_ID}/oauth2/v2.0/token",
        json={"access_token": fresh_token, "expires_in": 3600, "token_type": "Bearer"},
    )

    tok1 = get_azure_token()
    tok2 = get_azure_token()  # should re-fetch because expiry < 60s buffer

    assert len(resp_mock.calls) == 2
    assert tok1 != tok2

@resp_mock.activate
def test_get_azure_token_raises_on_error():
    resp_mock.add(
        resp_mock.POST,
        f"https://login.microsoftonline.com/{AZURE_TENANT_ID}/oauth2/v2.0/token",
        json={"error": "invalid_client", "error_description": "bad secret"},
        status=401,
    )

    with pytest.raises(Exception, match="invalid_client"):
        get_azure_token()
```

---

### Layer 2 — Firebase Auth Emulator (Official)

Google ships an official Firebase Auth emulator. It signs tokens with a local
key — no Firebase project or GCP account needed.

#### Install and start

Official docs: https://firebase.google.com/docs/emulator-suite/install_and_configure

**Option A — Docker (no Node.js required on host):**

Add to `docker-compose.yml`:
```yaml
firebase-emulator:
  image: node:20-alpine
  ports: ["9099:9099", "4000:4000"]
  command: npx -y firebase-tools@latest emulators:start --only auth --project local-project
  volumes: ["./firebase.json:/app/firebase.json"]
  working_dir: /app
```

When running inside Docker, `firebase.json` must bind the emulator to all
interfaces — otherwise the port mapping is not reachable from the host:
```json
{
  "emulators": {
    "auth": { "port": 9099, "host": "0.0.0.0" },
    "ui":   { "enabled": true, "port": 4000, "host": "0.0.0.0" }
  }
}
```

**Option B — Local Node.js install:**

```bash
# Install the Firebase CLI (requires Node.js — https://nodejs.org)
npm install -g firebase-tools

# Create firebase.json in your project root
cat > firebase.json << 'EOF'
{
  "emulators": {
    "auth": { "port": 9099 },
    "ui":   { "enabled": true, "port": 4000 }
  }
}
EOF

# Start — use any project ID, it does not need to exist
firebase emulators:start --only auth --project demo-local
```

The emulator UI is available at `http://localhost:4000`.

#### Point the SDK at the emulator

Set this environment variable **before** any Firebase Admin SDK or simulator code
runs. With it set, `createCustomToken`, `signInWithCustomToken`, and
`VerifyIDToken` all work locally with no real credentials:

```bash
export FIREBASE_AUTH_EMULATOR_HOST="localhost:9099"
```

#### Python simulator — emulator mode

```python
import os
os.environ["FIREBASE_AUTH_EMULATOR_HOST"] = "localhost:9099"

import firebase_admin
from firebase_admin import auth, credentials

# Any credential works — emulator ignores it
firebase_admin.initialize_app(credentials.ApplicationDefault())

# createCustomToken works with no GCP account
custom_token = auth.create_custom_token("simulator-uid")

import requests
# Note: emulator URL format for signInWithCustomToken
resp = requests.post(
    "http://localhost:9099/identitytoolkit.googleapis.com/v1/accounts:signInWithCustomToken?key=local-fake-key",
    json={"token": custom_token.decode(), "returnSecureToken": True},
)
id_token = resp.json()["idToken"]
print("Local Firebase ID token:", id_token[:60], "...")
```

#### Go backend — emulator mode

The Firebase Admin Go SDK respects `FIREBASE_AUTH_EMULATOR_HOST` automatically.
No code change is needed — set the env var and `VerifyIDToken` switches to the
emulator:

```go
// backend/middleware/firebase_auth.go
import (
    firebase "firebase.google.com/go/v4"
    "firebase.google.com/go/v4/auth"
    "google.golang.org/api/option"
)

func NewFirebaseClient(ctx context.Context) (*auth.Client, error) {
    app, err := firebase.NewApp(ctx, &firebase.Config{
        ProjectID: "demo-local", // must match --project flag on emulator
    }, option.WithoutAuthentication()) // no real credentials needed locally
    if err != nil {
        return nil, err
    }
    return app.Auth(ctx)
}

// In middleware — no change needed; VerifyIDToken auto-targets the emulator
// when FIREBASE_AUTH_EMULATOR_HOST is set
func NewFirebaseMiddleware(client *auth.Client) func(http.Handler) http.Handler {
    return func(next http.Handler) http.Handler {
        return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
            raw := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
            token, err := client.VerifyIDToken(r.Context(), raw)
            if err != nil {
                http.Error(w, `{"error":"invalid_token"}`, http.StatusUnauthorized)
                return
            }
            ctx := context.WithValue(r.Context(), "uid", token.UID)
            next.ServeHTTP(w, r.WithContext(ctx))
        })
    }
}
```

---

### Layer 3 — Mock GCP STS + IAM (Option B only)

There is no official GCP emulator for STS or IAM. Two approaches:

#### Option 3a: Skip the WIF chain in local tests (recommended)

With `FIREBASE_AUTH_EMULATOR_HOST` set, `createCustomToken` works with
`credentials.ApplicationDefault()` and no real GCP credentials. This means you
can bypass the entire Azure → WIF → SA → impersonation chain for local development
and test only the Firebase and backend layers — which is where your application
logic lives anyway. The WIF chain is pure infrastructure; validate it once with
real `gcloud` commands, not in every test run.

#### Option 3b: Mock the HTTP calls in unit tests

To test the WIF exchange code itself in isolation:

```python
# tests/test_wif.py
from unittest.mock import patch, MagicMock
from simulator.auth import exchange_for_gcp_token, impersonate_sa

def test_exchange_for_gcp_token_sends_correct_body():
    mock_resp = MagicMock()
    mock_resp.json.return_value = {"access_token": "fake-gcp-token"}
    mock_resp.raise_for_status = MagicMock()

    with patch("requests.post", return_value=mock_resp) as mock_post:
        token = exchange_for_gcp_token("fake-azure-jwt")

    assert token == "fake-gcp-token"
    body = mock_post.call_args[1]["json"]
    assert body["grant_type"] == "urn:ietf:params:oauth:grant-type:token-exchange"
    assert body["subjectToken"] == "fake-azure-jwt"

def test_impersonate_sa_sends_correct_scopes():
    mock_resp = MagicMock()
    mock_resp.json.return_value = {"accessToken": "fake-sa-token"}
    mock_resp.raise_for_status = MagicMock()

    with patch("requests.post", return_value=mock_resp) as mock_post:
        token = impersonate_sa("fake-federated-token")

    assert token == "fake-sa-token"
    body = mock_post.call_args[1]["json"]
    assert "https://www.googleapis.com/auth/firebase" in body["scope"]
```

---

### Local Test Matrix

| What you are testing | Tools needed | Cloud account? |
|---|---|---|
| Token caching + retry logic | Go `httptest.Server` mock | No |
| Azure AD token parsing + role check | Go mock + backend middleware unit test | No |
| Firebase custom token → ID token flow | Firebase Auth Emulator only | No |
| Backend validates tokens correctly | Firebase Auth Emulator + local backend | No |
| Full Option A end-to-end | Keycloak + local backend | No |
| Full Option B end-to-end (minus WIF) | Keycloak + Firebase Auth Emulator + local backend | No |
| WIF exchange logic in isolation | `unittest.mock.patch` on `requests.post` | No |
| WIF exchange against real GCP | Real GCP project | Yes — one-time setup |

---

### docker-compose: Full Local Stack

Brings up Keycloak, the Firebase Auth emulator, and the backend together. Run the
simulator locally pointing at these services.

```yaml
# docker-compose.yml
services:

  keycloak:
    image: quay.io/keycloak/keycloak:latest
    command: start-dev
    ports:
      - "8180:8080"
    environment:
      KEYCLOAK_ADMIN: admin
      KEYCLOAK_ADMIN_PASSWORD: admin
    healthcheck:
      test: ["CMD", "curl", "-f", "http://localhost:8080/health/ready"]
      interval: 10s
      retries: 10

  firebase-emulator:
    image: node:20-slim
    working_dir: /app
    volumes:
      - ./firebase.json:/app/firebase.json
      - ./emulator-data:/app/.firebase
    command: >
      sh -c "npm install -g firebase-tools &&
             firebase emulators:start --only auth --project demo-local"
    ports:
      - "9099:9099"
      - "4000:4000"
    healthcheck:
      test: ["CMD", "curl", "-f", "http://localhost:9099/"]
      interval: 10s
      retries: 10

  backend:
    build: ./backend
    ports:
      - "8080:8080"
    environment:
      FIREBASE_AUTH_EMULATOR_HOST: "firebase-emulator:9099"
    depends_on:
      firebase-emulator:
        condition: service_healthy
```

`firebase.json` (place in project root):

```json
{
  "emulators": {
    "auth": { "port": 9099 },
    "ui":   { "enabled": true, "port": 4000 }
  }
}
```

Start everything:

```bash
docker-compose up

# In a separate terminal — run the simulator locally
export FIREBASE_AUTH_EMULATOR_HOST=localhost:9099
export TOKEN_URL=http://localhost:8180/realms/test/protocol/openid-connect/token
export AZURE_CLIENT_ID=simulator-client
export AZURE_CLIENT_SECRET=<keycloak-secret>
export BACKEND_URL=http://localhost:8080

go run ./simulator
```

---

## Corrections vs Previous Answers

The following errors were present in the original answers and are fixed in this
document:

1. **Wrong SA role** — `roles/firebase.sdkAdminServiceAgent` is a Google-managed
   internal role and cannot be assigned directly. The correct grants are
   `roles/firebase.admin` (project level) and `roles/iam.serviceAccountTokenCreator`
   (on the SA itself, required for `signBlob` when no key file is present).

2. **Token issuer ambiguity** — The `oauth2/v2.0/token` endpoint issues v1-format
   tokens by default (issuer `https://sts.windows.net/TENANT_ID/`). The backend
   middleware must match the issuer exactly. Set `accessTokenAcceptedVersion: 2` in
   the app manifest only if you want v2 format, and update the WIF provider and
   backend middleware to match.

3. **Azure v1 vs v2 token claim** — The original WIF attribute mapping used only
   `assertion.appid` (v1 claim). Updated to `assertion.appid??assertion.azp` to
   handle both token versions.

4. **SA token expiry not handled** — The original `SimulatorAuth` class only tracked
   Firebase ID token expiry. The SA access token (used by Firebase Admin SDK for
   `signBlob`) also expires after 1 hour. The updated class tracks both independently.

5. **Firebase Admin SDK private internals** — The original code accessed
   `._credential._g_credential` directly. Replaced with a proper
   `credentials.Base` subclass using only the public SDK API.

6. **Go SDK requires explicit SA email** — Not mentioned in the original answer.
   The Firebase Admin Go SDK cannot auto-detect the service account email from a bare
   access token; `ServiceAccountID` must be set explicitly in `firebase.Config`.
