#!/usr/bin/env bash
# gcp-setup.sh — idempotent GCP setup for the WIF → Firebase test.
#
# Usage:
#   export GCP_PROJECT_ID=qwiklabs-gcp-04-abc123
#   bash scripts/gcp-setup.sh
#
# Re-run safely after any failure — every step is idempotent.
set -euo pipefail

# ── Required env vars ──────────────────────────────────────────────────────────
: "${GCP_PROJECT_ID:?Set GCP_PROJECT_ID to your lab project ID}"
: "${AZURE_TENANT_ID:?Set AZURE_TENANT_ID}"
: "${AZURE_CLIENT_ID:?Set AZURE_CLIENT_ID}"

POOL_ID="azure-pool"
PROVIDER_ID="azure-provider"
SA_NAME="simulator-sa"
SA_EMAIL="${SA_NAME}@${GCP_PROJECT_ID}.iam.gserviceaccount.com"

echo "==> Project: ${GCP_PROJECT_ID}"

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
echo ""
echo "Then get FIREBASE_API_KEY from:"
echo "  Firebase console → ${GCP_PROJECT_ID} → Project Settings → General → Web API Key"
echo ""
echo "If Firebase Auth is not yet enabled in the project:"
echo "  Firebase console → Authentication → Get started"
echo "============================================================"
