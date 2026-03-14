# Using an Existing Firebase Project

This guide is for when your Firebase project already exists — a permanent GCP project
where Firebase Auth is already enabled, rather than a short-lived Qwiklabs lab.

Compare with [README.md](README.md) which targets fresh lab projects.

---

## What is skipped vs the standard flow

| README step | Status with existing Firebase | Why |
|---|---|---|
| Step 1 — Set `GCP_PROJECT_ID` | **Still needed** | One-time export |
| Step 2 — Run `gcp-setup.sh` | **Still needed** | Creates WIF pool, provider, SA, and IAM bindings — none of these exist yet |
| **Step 3 — Enable Firebase Auth** | **SKIPPED** | Auth is already on; Web API Key already exists |
| Step 4 — Write `.env` | **Still needed** | But `FIREBASE_API_KEY` is already known — no console visit required |
| Step 5 — `go mod tidy` | **Still needed** | First-time only |
| Step 6 — Run the test | **Still needed** | |
| Step 7 — Unit tests | **Still needed** | |

**Summary: skip Step 3 entirely. Everything else is identical.**

---

## Step 1 — Set your project ID

```bash
export GCP_PROJECT_ID=my-permanent-project-id
```

---

## Step 2 — Run the GCP setup script

The script only touches IAM/WIF resources — it does not modify Firebase Auth or any
existing Firebase configuration.

```bash
export AZURE_TENANT_ID=<your-tenant-id>
export AZURE_CLIENT_ID=<your-client-id>

bash scripts/gcp-setup.sh
```

Safe to re-run if `azure-pool` or `simulator-sa` already exist — every resource
creation step is idempotent.

---

## Step 3 — Get your existing Web API Key  *(no console action needed)*

Your Web API Key already exists. Retrieve it one of two ways:

**Option A — Firebase console:**

> Project Settings (gear icon) → General tab → scroll to "Your apps" → Web API Key

**Option B — headless via gcloud:**

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

If no web app exists yet in the project, create one first (does not affect your
Firebase app or Auth settings):

```bash
curl -s -X POST \
  "https://firebase.googleapis.com/v1beta1/projects/$GCP_PROJECT_ID/webApps" \
  -H "Authorization: Bearer $(gcloud auth print-access-token)" \
  -H "Content-Type: application/json" \
  -d '{"displayName": "headless-wif-test"}'
# Wait ~5s, then run the APP_NAME / config commands above
```

---

## Step 4 — Write your .env

```bash
cp .env.example .env
```

Fill in values from `gcp-setup.sh` output plus the API key from Step 3:

```
AZURE_TENANT_ID=<your-tenant-id>
AZURE_CLIENT_ID=<your-client-id>
AZURE_CLIENT_SECRET=<your-secret>
TOKEN_URL=https://login.microsoftonline.com/<tenant-id>/oauth2/v2.0/token

GCP_PROJECT_ID=my-permanent-project-id
GCP_PROJECT_NUMBER=<from gcp-setup.sh output>
GCP_WIF_POOL=azure-pool
GCP_WIF_PROVIDER=azure-provider
GCP_SERVICE_ACCOUNT=simulator-sa@my-permanent-project-id.iam.gserviceaccount.com

FIREBASE_API_KEY=<from Step 3 above>
BACKEND_URL=http://localhost:8080
```

```bash
source .env
```

---

## Steps 5–7 — Same as README

```bash
# Dependencies (first time only)
cd backend  && go mod tidy && cd ..
cd simulator && go mod tidy && cd ..

# Terminal 1 — start backend
cd backend && GCP_PROJECT_ID=$GCP_PROJECT_ID go run .

# Terminal 2 — run simulator
cd simulator && go run .

# Unit tests (no GCP needed)
cd simulator && go test ./...
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
    [option-b] no token          →  401
All Option B assertions passed.
```

---

## Does this affect my existing Firebase project?

| Resource created | Where | Impact on existing Firebase config |
|---|---|---|
| `azure-pool` WIF pool | GCP IAM | None — new IAM resource only |
| `azure-provider` OIDC provider | GCP IAM | None — new IAM resource only |
| `simulator-sa` service account | GCP IAM | None — new SA only |
| `simulator-sa` Firebase Auth Admin role | Project IAM | Lets the SA mint custom tokens; does not change Auth settings |
| Firebase custom token + ID token | Runtime only | Tokens are short-lived (1 h); no persistent users are created unless your backend stores them |

Nothing in `gcp-setup.sh` modifies Firebase Authentication settings, sign-in providers,
existing users, or any other Firebase product.
