# End-to-End Walkthrough — WIF + Firebase Headless Simulator

A complete reference for understanding, running, and debugging the simulator.
Read this if you want to know *why* each step exists, not just *what* to run.

---

## The problem being solved

The real system has a human clicking a browser. This simulator replaces that human
so automated tests can call the same backend API with a valid Firebase JWT —
no browser, no user interaction.

---

## The full chain — every hop

```
┌─────────────────────────────────────────────────────────────┐
│  1. Azure AD                                                │
│     client_credentials → access token (JWT)                │
│     Who:  your app registration                             │
│     Why:  proves "I am the simulator app"                   │
└────────────────────────┬────────────────────────────────────┘
                         │ Azure JWT
┌────────────────────────▼────────────────────────────────────┐
│  2. GCP STS  (sts.googleapis.com)                           │
│     WIF token exchange → GCP federated token               │
│     Who:  Workload Identity Pool  (azure-pool)              │
│     Why:  converts Azure identity into a GCP identity       │
│     Key:  attribute-condition locks this to your            │
│           AZURE_CLIENT_ID — no other app can exchange       │
└────────────────────────┬────────────────────────────────────┘
                         │ GCP federated token
┌────────────────────────▼────────────────────────────────────┐
│  3. IAM Credentials  (iamcredentials.googleapis.com)        │
│     generateAccessToken → simulator-sa access token        │
│     Who:  simulator-sa service account                      │
│     Why:  need a real SA token to call Google APIs          │
└────────────────────────┬────────────────────────────────────┘
                         │ SA access token
┌────────────────────────▼────────────────────────────────────┐
│  4. IAM signJwt  (iamcredentials.googleapis.com)            │
│     Signs a Firebase custom token payload                   │
│     Who:  simulator-sa  (has serviceAccountTokenCreator)    │
│     Why:  no private key file needed — SA signs via API     │
│     Payload includes: uid + your custom claims              │
│     e.g.  {"htpa_roles": ["HTPA_USER"]}                     │
└────────────────────────┬────────────────────────────────────┘
                         │ signed Firebase custom JWT
┌────────────────────────▼────────────────────────────────────┐
│  5. Firebase Identity Toolkit                               │
│     signInWithCustomToken → Firebase ID token              │
│     Who:  Firebase Auth (your project)                      │
│     Why:  custom token → real signed Firebase ID token      │
│     Claims from step 4 are preserved in the ID token        │
└────────────────────────┬────────────────────────────────────┘
                         │ Firebase ID token  (~1 h expiry)
          ┌──────────────▼──────────────────────────┐
          │          TWO MODES FROM HERE             │
          └──────────────────────────────────────────┘
```

---

## Mode 1 — Direct (no Apigee)

```
Simulator ──→  Authorization: Bearer <firebase-jwt>  ──→  Backend
                                                            │
                                                       validates JWT
                                                       against Firebase
                                                       public JWKS
```

Use for: testing the backend in isolation, CI/CD pipelines, local development.

---

## Mode 2 — Via Apigee (production path)

```
Simulator ──→  Authorization: Bearer <apigee-token>  ──→  Apigee
               X-FB-TOKEN: <firebase-jwt>                   │
                                               validates apigee token
                                               replaces Bearer with
                                               X-FB-TOKEN value
                                                            │
                                                            ▼
                                                         Backend
                                               Authorization: Bearer
                                               <firebase-jwt>
```

Use for: full production path testing — identical headers to what the real
Angular app sends through the OnPrefetch Cloud Function.

Run with:

```bash
go run . --via-apigee
```

---

## Every environment variable and why it exists

```bash
# ── Azure ──────────────────────────────────────────────────────────────────────
AZURE_TENANT_ID       # your Azure directory ID — tells STS which tenant to trust
AZURE_CLIENT_ID       # app registration client ID — used as WIF audience check
AZURE_CLIENT_SECRET   # credential for client_credentials grant
TOKEN_URL             # Azure token endpoint for step 1

# ── GCP ────────────────────────────────────────────────────────────────────────
GCP_PROJECT_ID        # Firebase project ID — used in JWT issuer + audience validation
GCP_PROJECT_NUMBER    # numeric ID — used in the WIF audience string for step 2
GCP_WIF_POOL          # "azure-pool" — the WIF pool created by gcp-setup.sh
GCP_WIF_PROVIDER      # "azure-provider" — the OIDC provider inside that pool
GCP_SERVICE_ACCOUNT   # "simulator-sa@..." — the SA impersonated in step 3

# ── Firebase ───────────────────────────────────────────────────────────────────
FIREBASE_API_KEY      # Web API key — required by signInWithCustomToken (step 5)
FIREBASE_CUSTOM_CLAIMS # JSON claims embedded in the token
                       # e.g. {"htpa_roles":["HTPA_USER"]}
                       # must match what your FetchGroup CF writes

# ── Backend ────────────────────────────────────────────────────────────────────
BACKEND_URL           # where the simulator sends API calls
BACKEND_REQUIRED_ROLE # optional role enforcement: "field:value"
                       # e.g. "htpa_roles:HTPA_USER"
                       # backend returns 403 if claim is missing

# ── Apigee  (only required when running with --via-apigee) ─────────────────────
APIGEE_TOKEN_URL      # AUTH_ENDPOINT — where the simulator fetches the Apigee token
APIGEE_CLIENT_ID      # Apigee app client ID
APIGEE_CLIENT_SECRET  # Apigee app client secret
APIGEE_AUTH_SCHEME    # "basic" (Apigee Edge default) or "body" (standard OAuth 2.0)
APIGEE_SCOPE          # optional scope — leave empty if not required by your proxy
```

---

## GCP resources — what exists and who created it

| Resource | Created by | Purpose |
|---|---|---|
| WIF pool `azure-pool` | `gcp-setup.sh` | Trusts tokens issued by Azure AD |
| WIF provider `azure-provider` | `gcp-setup.sh` | Maps Azure OIDC → GCP identity; locked to your `AZURE_CLIENT_ID` |
| SA `simulator-sa` | `gcp-setup.sh` | The GCP identity the simulator impersonates |
| `workloadIdentityUser` binding | `gcp-setup.sh` | Allows the WIF pool to impersonate the SA |
| `serviceAccountTokenCreator` binding | `gcp-setup.sh` | Allows the SA to sign JWTs (step 4) |
| `firebaseauth.admin` binding | `gcp-setup.sh` | Allows the SA's custom tokens to be accepted by Firebase |
| Firebase project | `gcp-setup.sh --new-firebase` | Connects Firebase to the GCP project |
| Firebase Auth | **Firebase console only** | Enables `signInWithCustomToken` — one manual click |

---

## Step-by-step: first run on a new lab project

```bash
# 1. Set project and Azure vars
export GCP_PROJECT_ID=qwiklabs-gcp-xx-xxxxxxxxxxxx
export AZURE_TENANT_ID=<your-tenant-id>
export AZURE_CLIENT_ID=<your-client-id>

# 2. Run setup — answer "new" when asked about Firebase
bash scripts/gcp-setup.sh
# Prints: GCP_PROJECT_NUMBER, GCP_SERVICE_ACCOUNT, FIREBASE_API_KEY

# 3. Enable Firebase Auth  (one manual click — cannot be automated)
#    https://console.firebase.google.com → your project
#    → Authentication → Get started

# 4. Fill in .env
cp .env.example .env
# Paste the values printed by gcp-setup.sh
# Add AZURE_CLIENT_SECRET
source .env

# 5. Fetch Go dependencies  (first time only)
cd backend  && go mod tidy && cd ..
cd simulator && go mod tidy && cd ..

# 6. Start the backend  (terminal 1)
cd backend
GCP_PROJECT_ID=$GCP_PROJECT_ID go run .

# 7. Run the simulator  (terminal 2)
cd simulator
go run .               # direct — Firebase JWT straight to backend
go run . --via-apigee  # via Apigee — matches production header flow
```

---

## Reading the output

```
[option-b] fetching Azure token...           OK
  ↑ step 1 succeeded — Azure accepted client_credentials

[option-b] WIF STS exchange...               OK
  ↑ step 2 succeeded — WIF pool recognised and trusted the Azure JWT

[option-b] SA impersonation...               OK
  ↑ step 3 succeeded — WIF→SA binding is in place

[option-b] minting Firebase custom token...  OK
    claims injected: {"htpa_roles":["HTPA_USER"]}
  ↑ step 4 succeeded — SA signed the JWT; claims are embedded

[option-b] exchanging for Firebase ID token  OK
    id token claims: {"htpa_roles":["HTPA_USER"]}
  ↑ step 5 succeeded — Firebase Auth is enabled; claims survived exchange

[option-b] fetching Apigee token...          OK       ← --via-apigee only
    route: Authorization=Apigee token  X-FB-TOKEN=Firebase JWT

    [option-b] POST /api/users  →  201   ← backend accepted the token and role
    [option-b] GET  /api/users  →  200
    [option-b] no token          →  401   ← backend correctly rejects missing token

All Option B assertions passed.
```

---

## Failure map — where it breaks and how to fix it

| Fails at | Error | Cause | Fix |
|---|---|---|---|
| Azure token | `invalid_client` | Wrong client secret | Check `AZURE_CLIENT_SECRET` |
| WIF STS exchange | `invalid_grant` | Pool/provider not configured, or Azure token audience mismatch | Re-run `gcp-setup.sh`; verify `AZURE_CLIENT_ID` matches provider `--allowed-audiences` |
| SA impersonation | `permission denied` | WIF→SA binding missing, or wrong `GCP_PROJECT_NUMBER` | Re-run `gcp-setup.sh`; verify project number |
| signJwt | `403` | SA missing `serviceAccountTokenCreator` on itself | Re-run `gcp-setup.sh` |
| Firebase ID token | `CONFIGURATION_NOT_FOUND` | Firebase Auth not enabled | Firebase console → Authentication → Get started |
| Firebase ID token | `INVALID_CUSTOM_TOKEN` | SA email in JWT payload doesn't match signing SA | Check `GCP_SERVICE_ACCOUNT` is correct |
| Firebase ID token | `INVALID_AUDIENCE` | Backend `GCP_PROJECT_ID` doesn't match Firebase project | Start backend with the correct `GCP_PROJECT_ID` |
| Apigee token | `invalid_client` | Wrong Apigee credentials | Check `APIGEE_CLIENT_ID` / `APIGEE_CLIENT_SECRET` |
| POST / GET → `401` | `unauthorized` | Firebase JWT not reaching backend as Bearer | Check Apigee token-replacement policy; restart backend if JWKS cache is stale |
| POST / GET → `403` | `missing required role` | `FIREBASE_CUSTOM_CLAIMS` not set or wrong field name | Set `FIREBASE_CUSTOM_CLAIMS` to match what your FetchGroup CF writes |

---

## New lab project? 3-minute reset

```bash
export GCP_PROJECT_ID=<new-lab-project-id>
bash scripts/gcp-setup.sh   # answer "new" — recreates all resources
# click Authentication → Get started in Firebase console
# update GCP_PROJECT_ID, GCP_PROJECT_NUMBER, GCP_SERVICE_ACCOUNT, FIREBASE_API_KEY in .env
source .env
```

Azure credentials, Apigee credentials, and custom claims stay the same across labs.

---

## Existing Firebase project? Skip step 3

```bash
bash scripts/gcp-setup.sh --existing-firebase
# Firebase Auth already on — no console action needed
# API key is retrieved automatically and printed in the .env block
```

See [EXISTING-FIREBASE.md](EXISTING-FIREBASE.md) for the full guide.
