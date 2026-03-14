# Headless Simulator — Option B WIF + Firebase (Real GCP)

End-to-end test of the full Azure → WIF → Firebase token chain using a real
Google Cloud project. No emulators. Works with any temporary lab project.

```
Simulator
    │
    ▼  client_credentials
Azure AD  (your existing app registration)
    │
    ▼  WIF STS token exchange  (sts.googleapis.com)
GCP Federated Access Token
    │
    ▼  SA impersonation  (iamcredentials.googleapis.com)
Service Account Access Token
    │
    ▼  IAM signJwt  (iamcredentials.googleapis.com)
Firebase Custom Token  (signed by SA — no key file needed)
    │
    ▼  signInWithCustomToken  (identitytoolkit.googleapis.com)
Firebase ID Token
    │
    ▼  Bearer header
Backend :8080  (validates against securetoken.google.com JWKS)
```

---

## Prerequisites

| Tool | Notes |
|---|---|
| Go >= 1.21 | https://go.dev/dl/ |
| `gcloud` CLI | https://cloud.google.com/sdk/docs/install |
| Google Cloud lab / project | Any project where you have `roles/owner` |
| Azure app registration | Reuse the one from `headless-test/` |

---

## Step 1 — Set your lab project ID

Every time you start a new lab, only this value changes:

```bash
export GCP_PROJECT_ID=qwiklabs-gcp-04-abc123def456
```

---

## Step 2 — Run the GCP setup script

The script is fully idempotent — safe to re-run after any failure:

```bash
# Azure vars must also be set (reuse from headless-test/)
export AZURE_TENANT_ID=fd753b92-f3b7-49fa-95cf-aed075c0f21c
export AZURE_CLIENT_ID=7daa4558-625d-4277-8dc7-46cf5ab009c8

bash scripts/gcp-setup.sh
```

The script:
1. Resolves the project number automatically
2. Enables IAM, STS, IAM Credentials, and Identity Toolkit APIs
3. Creates the Workload Identity Pool `azure-pool` with OIDC provider `azure-provider`
   — trusts your Azure tenant, allows only your simulator's `appid`
4. Creates service account `simulator-sa`
5. Binds the WIF pool → SA impersonation
6. Grants `roles/firebaseauth.admin` and Token Creator to the SA
7. Prints the `.env` values to copy

---

## Step 3 — Enable Firebase Auth

The setup script cannot enable Firebase Authentication via `gcloud` — this step requires
the Firebase console:

1. Go to https://console.firebase.google.com
2. Select (or add) your lab project
3. Click **Authentication → Get started**
4. No sign-in providers needed — custom tokens work without any provider enabled

**Get the Web API Key** after enabling Auth:

- Firebase console → **Project Settings → General → Web API Key**
- Or, if you already have a Firebase web app registered, get it headlessly:

```bash
APP_NAME=$(curl -s \
  "https://firebase.googleapis.com/v1beta1/projects/$GCP_PROJECT_ID/webApps" \
  -H "Authorization: Bearer $(gcloud auth print-access-token)" \
  | python3 -c "import sys,json; print(json.load(sys.stdin)['apps'][0]['name'])")

curl -s \
  "https://firebase.googleapis.com/v1beta1/${APP_NAME}/config" \
  -H "Authorization: Bearer $(gcloud auth print-access-token)" \
  | python3 -c "import sys,json; print(json.load(sys.stdin)['apiKey'])"
```

---

## Step 4 — Write your .env

```bash
cp .env.example .env
```

Fill in the values printed by `gcp-setup.sh` plus `FIREBASE_API_KEY` from the
Firebase console. Example for a typical Qwiklabs project:

```
AZURE_TENANT_ID=fd753b92-f3b7-49fa-95cf-aed075c0f21c
AZURE_CLIENT_ID=7daa4558-625d-4277-8dc7-46cf5ab009c8
AZURE_CLIENT_SECRET=<your-secret>
TOKEN_URL=https://login.microsoftonline.com/fd753b92-f3b7-49fa-95cf-aed075c0f21c/oauth2/v2.0/token

GCP_PROJECT_ID=qwiklabs-gcp-04-abc123def456
GCP_PROJECT_NUMBER=123456789012
GCP_WIF_POOL=azure-pool
GCP_WIF_PROVIDER=azure-provider
GCP_SERVICE_ACCOUNT=simulator-sa@qwiklabs-gcp-04-abc123def456.iam.gserviceaccount.com

FIREBASE_API_KEY=AIzaSyAbc123...
BACKEND_URL=http://localhost:8080
```

Load it:

```bash
source .env
```

---

## Step 5 — Fetch Go dependencies

```bash
cd backend  && go mod tidy && cd ..
cd simulator && go mod tidy && cd ..
```

---

## Step 6 — Run the test

```bash
# Terminal 1 — start backend
cd backend
GCP_PROJECT_ID=$GCP_PROJECT_ID go run .

# Terminal 2 — run simulator
cd simulator
go run .
```

Expected output:

```
[option-b] fetching Azure token...           OK
[option-b] WIF STS exchange...               OK
[option-b] SA impersonation...               OK
[option-b] minting Firebase custom token...  OK
[option-b] exchanging for Firebase ID token  OK
    [option-b] POST /api/users  →  201
    [option-b] GET  /api/users  →  200
    [option-b] no token         →  401
All Option B assertions passed.
```

---

## Step 7 — Run unit tests (no GCP needed)

```bash
cd simulator && go test ./...
```

All 4 tests in `wif/` use `httptest` mocks — they pass without any GCP credentials.

---

## New lab? Just do this

```bash
export GCP_PROJECT_ID=<new-lab-project-id>
bash scripts/gcp-setup.sh     # recreates all resources in the new project
# update GCP_PROJECT_ID, GCP_PROJECT_NUMBER, GCP_SERVICE_ACCOUNT in .env
# re-enable Firebase Auth and get the new Web API Key
source .env
cd backend && go run . &
cd ../simulator && go run .
```

---

## Troubleshooting

| Symptom | Cause | Fix |
|---|---|---|
| `invalid_grant` from STS | WIF pool/provider not configured, or Azure token audience mismatch | Re-run `gcp-setup.sh`; check `AZURE_CLIENT_ID` matches provider's `--allowed-audiences` |
| `permission denied` on `generateAccessToken` | WIF → SA binding missing, or wrong project number | Re-run `gcp-setup.sh`; verify `GCP_PROJECT_NUMBER` is correct |
| `signJwt` 403 | SA missing `roles/iam.serviceAccountTokenCreator` on itself | Re-run `gcp-setup.sh` |
| `INVALID_CUSTOM_TOKEN` from Firebase | SA email in JWT payload doesn't match the signing SA | Check `GCP_SERVICE_ACCOUNT` is set correctly |
| `INVALID_AUDIENCE` from backend | `GCP_PROJECT_ID` in backend doesn't match Firebase project | Ensure backend is started with correct `GCP_PROJECT_ID` |
| Firebase token signature invalid | Backend JWKS cache stale | Restart the backend |
| `APIs not enabled` | New lab project needs API activation | Re-run `gcp-setup.sh` (it enables APIs automatically) |
| `CONFIGURATION_NOT_FOUND` from Firebase | Firebase Authentication not initialized | Go to Firebase console → Authentication → Get started |
