#!/usr/bin/env bash
# gcp-setup.sh — idempotent GCP setup for the WIF → Firebase test.
#
# Usage:
#   export GCP_PROJECT_ID=qwiklabs-gcp-04-abc123
#   bash scripts/gcp-setup.sh
#
#   Flags (skip the interactive prompt):
#     --new-firebase       Firebase not yet enabled — provision it and print console instructions
#     --existing-firebase  Firebase already enabled — skip provisioning, print API key command only
#
# Re-run safely after any failure — every step is idempotent.
set -euo pipefail

# ── Parse flags ────────────────────────────────────────────────────────────────
FIREBASE_MODE=""   # "new" | "existing" | "" (ask)
for arg in "$@"; do
  case "$arg" in
    --new-firebase)      FIREBASE_MODE="new"      ;;
    --existing-firebase) FIREBASE_MODE="existing" ;;
    *) echo "Unknown flag: $arg"; exit 1 ;;
  esac
done

# ── Required env vars ──────────────────────────────────────────────────────────
: "${GCP_PROJECT_ID:?Set GCP_PROJECT_ID to your lab project ID}"
: "${AZURE_TENANT_ID:?Set AZURE_TENANT_ID}"
: "${AZURE_CLIENT_ID:?Set AZURE_CLIENT_ID}"

POOL_ID="azure-pool"
PROVIDER_ID="azure-provider"
SA_NAME="simulator-sa"
SA_EMAIL="${SA_NAME}@${GCP_PROJECT_ID}.iam.gserviceaccount.com"

echo "==> Project: ${GCP_PROJECT_ID}"

# ── Ask about Firebase if no flag given ───────────────────────────────────────
if [[ -z "$FIREBASE_MODE" ]]; then
  echo ""
  echo "Is Firebase already enabled on this project?"
  echo "  (Choose 'existing' if Firebase Auth is on and you have a Web API Key)"
  echo "  (Choose 'new' for a fresh lab project or first-time setup)"
  echo ""
  read -r -p "Firebase status [new/existing]: " FIREBASE_ANSWER
  case "${FIREBASE_ANSWER,,}" in
    existing|e|yes|y) FIREBASE_MODE="existing" ;;
    new|n)            FIREBASE_MODE="new"      ;;
    *)
      echo "Unrecognised answer '${FIREBASE_ANSWER}'. Use 'new' or 'existing'."
      exit 1
      ;;
  esac
fi

echo "==> Firebase mode: ${FIREBASE_MODE}"
echo ""

# ── Resolve project number ─────────────────────────────────────────────────────
echo "==> Resolving project number..."
PROJECT_NUMBER=$(gcloud projects describe "${GCP_PROJECT_ID}" \
  --format="value(projectNumber)")
echo "    Project number: ${PROJECT_NUMBER}"

# ── Enable required APIs ───────────────────────────────────────────────────────
echo "==> Enabling APIs (this takes ~30s on a fresh project)..."
gcloud services enable \
  iam.googleapis.com \
  iamcredentials.googleapis.com \
  sts.googleapis.com \
  identitytoolkit.googleapis.com \
  firebase.googleapis.com \
  --project="${GCP_PROJECT_ID}"

# ── Workload Identity Pool ─────────────────────────────────────────────────────
echo "==> Creating Workload Identity Pool '${POOL_ID}'..."
gcloud iam workload-identity-pools create "${POOL_ID}" \
  --location="global" \
  --display-name="Azure AD Pool" \
  --project="${GCP_PROJECT_ID}" 2>/dev/null \
  || echo "    (pool already exists, continuing)"

# ── OIDC Provider (trusts your Azure tenant) ───────────────────────────────────
echo "==> Creating OIDC provider '${PROVIDER_ID}'..."
# Azure v1 tokens use sts.windows.net issuer.
gcloud iam workload-identity-pools providers create-oidc "${PROVIDER_ID}" \
  --location="global" \
  --workload-identity-pool="${POOL_ID}" \
  --issuer-uri="https://sts.windows.net/${AZURE_TENANT_ID}/" \
  --allowed-audiences="${AZURE_CLIENT_ID}" \
  --attribute-mapping="google.subject=assertion.sub,attribute.appid=assertion.appid" \
  --attribute-condition="attribute.appid==\"${AZURE_CLIENT_ID}\"" \
  --project="${GCP_PROJECT_ID}" 2>/dev/null \
  || echo "    (provider already exists, continuing)"

# ── Service Account ────────────────────────────────────────────────────────────
echo "==> Creating service account '${SA_NAME}'..."
gcloud iam service-accounts create "${SA_NAME}" \
  --display-name="Headless Simulator WIF SA" \
  --project="${GCP_PROJECT_ID}" 2>/dev/null \
  || echo "    (service account already exists, continuing)"

# ── Allow WIF pool to impersonate the SA ──────────────────────────────────────
echo "==> Binding WIF pool → SA impersonation..."
gcloud iam service-accounts add-iam-policy-binding "${SA_EMAIL}" \
  --member="principalSet://iam.googleapis.com/projects/${PROJECT_NUMBER}/locations/global/workloadIdentityPools/${POOL_ID}/*" \
  --role="roles/iam.workloadIdentityUser" \
  --project="${GCP_PROJECT_ID}"

# ── Firebase Auth Admin on the SA ─────────────────────────────────────────────
echo "==> Granting Firebase Auth Admin to SA..."
gcloud projects add-iam-policy-binding "${GCP_PROJECT_ID}" \
  --member="serviceAccount:${SA_EMAIL}" \
  --role="roles/firebaseauth.admin"

# ── Also needs iam.serviceAccounts.signJwt (for custom token minting) ─────────
echo "==> Granting Service Account Token Creator to SA (needed for signJwt)..."
gcloud iam service-accounts add-iam-policy-binding "${SA_EMAIL}" \
  --member="serviceAccount:${SA_EMAIL}" \
  --role="roles/iam.serviceAccountTokenCreator" \
  --project="${GCP_PROJECT_ID}"

# ── Firebase provisioning (new projects only) ─────────────────────────────────
if [[ "$FIREBASE_MODE" == "new" ]]; then
  echo "==> Adding Firebase to project (safe to re-run)..."
  TOKEN=$(gcloud auth print-access-token)
  ADD_RESULT=$(curl -s -o /dev/null -w "%{http_code}" -X POST \
    "https://firebase.googleapis.com/v1beta1/projects/${GCP_PROJECT_ID}:addFirebase" \
    -H "Authorization: Bearer ${TOKEN}" \
    -H "Content-Type: application/json" \
    -d '{}')
  if [[ "$ADD_RESULT" == "200" ]]; then
    echo "    Firebase added to project."
  elif [[ "$ADD_RESULT" == "409" ]]; then
    echo "    (Firebase already added, continuing)"
  else
    echo "    WARNING: addFirebase returned HTTP ${ADD_RESULT} — check console if test fails"
  fi

  echo "==> Creating Firebase web app (needed to generate Web API Key)..."
  TOKEN=$(gcloud auth print-access-token)
  APP_HTTP=$(curl -s -o /tmp/_fb_app.json -w "%{http_code}" -X POST \
    "https://firebase.googleapis.com/v1beta1/projects/${GCP_PROJECT_ID}/webApps" \
    -H "Authorization: Bearer ${TOKEN}" \
    -H "Content-Type: application/json" \
    -d '{"displayName": "headless-wif-test"}')
  if [[ "$APP_HTTP" == "200" ]]; then
    echo "    Web app created. Waiting 5s for propagation..."
    sleep 5
  else
    echo "    (web app may already exist, HTTP ${APP_HTTP} — continuing)"
  fi
fi

# ── Retrieve Firebase Web API Key ──────────────────────────────────────────────
echo "==> Retrieving Firebase Web API Key..."
TOKEN=$(gcloud auth print-access-token)
APPS_JSON=$(curl -s \
  "https://firebase.googleapis.com/v1beta1/projects/${GCP_PROJECT_ID}/webApps" \
  -H "Authorization: Bearer ${TOKEN}")

APP_NAME=$(echo "${APPS_JSON}" | python3 -c \
  "import sys,json; apps=json.load(sys.stdin).get('apps',[]); print(apps[0]['name'] if apps else '')" 2>/dev/null || true)

FIREBASE_API_KEY=""
if [[ -n "$APP_NAME" ]]; then
  FIREBASE_API_KEY=$(curl -s \
    "https://firebase.googleapis.com/v1beta1/${APP_NAME}/config" \
    -H "Authorization: Bearer ${TOKEN}" \
    | python3 -c "import sys,json; print(json.load(sys.stdin).get('apiKey',''))" 2>/dev/null || true)
fi

# ── Print .env values ──────────────────────────────────────────────────────────
echo ""
echo "============================================================"
echo "GCP setup complete. Add these to your .env file:"
echo ""
echo "GCP_PROJECT_ID=${GCP_PROJECT_ID}"
echo "GCP_PROJECT_NUMBER=${PROJECT_NUMBER}"
echo "GCP_WIF_POOL=${POOL_ID}"
echo "GCP_WIF_PROVIDER=${PROVIDER_ID}"
echo "GCP_SERVICE_ACCOUNT=${SA_EMAIL}"
if [[ -n "$FIREBASE_API_KEY" ]]; then
  echo "FIREBASE_API_KEY=${FIREBASE_API_KEY}"
else
  echo "FIREBASE_API_KEY=<get from Firebase console → Project Settings → General>"
fi
echo ""

if [[ "$FIREBASE_MODE" == "new" ]]; then
  echo "ACTION REQUIRED — Firebase Auth must be enabled manually:"
  echo "  1. Go to https://console.firebase.google.com/project/${GCP_PROJECT_ID}/authentication"
  echo "  2. Click 'Get started'"
  echo "  3. No providers needed — just click through"
elif [[ "$FIREBASE_MODE" == "existing" ]]; then
  echo "Firebase Auth already enabled — no console action needed."
fi
echo "============================================================"
