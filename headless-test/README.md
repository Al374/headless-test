# Headless Simulator — Local Test Environment

Local end-to-end test for the headless simulator authentication flow.
No Azure, GCP, or Firebase account required.

## What This Tests

```
Simulator (Go)
    │
    ▼
Keycloak :8180            ← replaces Azure Entra ID  (Docker)
    │
    ▼
Firebase Emulator :9099   ← replaces Firebase Auth   (Docker, Option B only)
    │
    ▼
Backend :8080             ← minimal Go API with JWT middleware (go run .)
```

> Default backend port is `8080`. Override with `BACKEND_PORT` + `BACKEND_URL`
> if that port is already in use on your machine.

## Prerequisites

| Tool | Version | Install |
|---|---|---|
| Docker + Docker Compose | any recent | https://docs.docker.com/get-docker/ |
| Go | >= 1.21 | https://go.dev/dl/ |
| `curl` + `jq` | any | system package manager |
| PowerShell | >= 7.0 (Windows) | https://aka.ms/powershell (pre-installed on Windows 10+) |

> **Node.js and Firebase CLI are not required on the host.** The Firebase Auth
> Emulator runs inside the `firebase-emulator` Docker container defined in
> `docker-compose.yml`, so no local Node installation is needed.

---

## Quick Start

### 1. Start the local services

```bash
# Bash / macOS / Linux
docker-compose up -d
```

```powershell
# PowerShell (Windows)
docker-compose up -d
```

> `docker-compose` is cross-platform — the command is identical. Ensure Docker
> Desktop is running before executing this step.

Wait for Keycloak to be ready (takes ~20s). Poll until you get HTTP 200:

```bash
# Bash / macOS / Linux
until curl -sf http://localhost:8180/realms/master > /dev/null; do
  echo "waiting for Keycloak..."; sleep 3
done
echo "Keycloak ready"
```

```powershell
# PowerShell (Windows)
do {
  Start-Sleep 3
  Write-Host "waiting for Keycloak..."
} until ((Invoke-WebRequest -Uri http://localhost:8180/realms/master -UseBasicParsing -ErrorAction SilentlyContinue).StatusCode -eq 200)
Write-Host "Keycloak ready"
```

> The `/health/ready` endpoint returns 404 on Keycloak 24 in dev mode.
> Polling `/realms/master` (HTTP 200) is the reliable indicator.

---

### 2. Configure Keycloak

```bash
# Bash / macOS / Linux
bash scripts/keycloak-setup.sh
```

```powershell
# PowerShell (Windows)
.\scripts\keycloak-setup.ps1
```

> Both scripts do the same thing: create the `test` realm, `simulator-client`
> (with client credentials enabled), the `Backend.Call` app role, and the
> `backend-api` audience mapper. The client secret is printed at the end —
> copy it into your `.env` / `.env.ps1` file.

---

### 3. Set environment variables

```bash
# Bash / macOS / Linux
cp .env.example .env
# Edit .env and paste the client secret from step 2
source .env
```

```powershell
# PowerShell (Windows)
Copy-Item .env.example .env
# Edit .env and paste the client secret from step 2, then load it:
Get-Content .env | ForEach-Object {
    if ($_ -match '^\s*([^#][^=]+)=(.*)$') {
        [System.Environment]::SetEnvironmentVariable($matches[1].Trim(), $matches[2].Trim(), 'Process')
    }
}
```

> The `.env` file format is `KEY=VALUE` (one per line, `#` for comments).
> PowerShell does not have a native `source` command — the snippet above
> parses and loads each variable into the current process scope.

---

### 4. Fetch Go dependencies (first time only)

```bash
# Bash / macOS / Linux
cd backend  && go mod tidy && cd ..
cd simulator && go mod tidy && cd ..
```

```powershell
# PowerShell (Windows)
Set-Location backend;  go mod tidy; Set-Location ..
Set-Location simulator; go mod tidy; Set-Location ..
```

---

### 5. Run Option A test (Azure AD direct)

```bash
# Bash / macOS / Linux — start backend in background, then run simulator
cd backend && go run . &
cd ../simulator && go run . --option=a
```

```powershell
# PowerShell (Windows) — start backend in a background job
$backend = Start-Job -ScriptBlock { Set-Location "$using:PWD\backend"; go run . }
Set-Location simulator
go run . --option=a
```

> If port `8080` is already in use on your machine, set `BACKEND_PORT` and
> `BACKEND_URL` to a free port before starting the backend:
>
> ```bash
> export BACKEND_PORT=8081
> export BACKEND_URL=http://localhost:8081
> cd backend && go run . &
> ```

Expected output:
```
[option-a] fetching token from Keycloak...  OK
[option-a] POST /api/users  →  201
[option-a] GET  /api/users  →  200
[option-a] no token         →  401
All Option A assertions passed.
```

---

### 6. Run Option B test (Firebase token)

```bash
# Bash / macOS / Linux
export FIREBASE_AUTH_EMULATOR_HOST=localhost:9099
cd simulator && go run . --option=b
```

```powershell
# PowerShell (Windows)
$env:FIREBASE_AUTH_EMULATOR_HOST = "localhost:9099"
Set-Location simulator
go run . --option=b
```

Expected output:
```
[option-b] fetching Keycloak token...        OK
[option-b] minting Firebase custom token...  OK
[option-b] exchanging for ID token...        OK
[option-b] POST /api/users  →  201
[option-b] GET  /api/users  →  200
[option-b] no token         →  401
All Option B assertions passed.
```

---

### 7. Run unit tests

```bash
# Bash / macOS / Linux
cd simulator && go test ./...
```

```powershell
# PowerShell (Windows)
Set-Location simulator
go test ./...
```

---

## Stopping Everything

```bash
# Bash / macOS / Linux
docker-compose down
kill %1   # stop the backend background process
```

```powershell
# PowerShell (Windows)
docker-compose down
Stop-Job $backend        # stop the background job started in step 4
Remove-Job $backend
```

> If you started the backend manually in a separate terminal rather than as a
> background job, just close that terminal or press `Ctrl+C` in it.

---

## Troubleshooting

| Symptom | Cause | Fix (Bash) | Fix (PowerShell) |
|---|---|---|---|
| `connection refused :8180` | Keycloak still starting | Poll `curl http://localhost:8180/realms/master` until 200 | Poll with `Invoke-WebRequest` (see step 1) |
| `invalid_client` from Keycloak | Wrong client secret in `.env` | Re-run `bash scripts/keycloak-setup.sh` and update `.env` | Re-run `.\scripts\keycloak-setup.ps1` |
| `invalid_token` from backend | Wrong issuer or audience | Check `AZURE_TENANT_ID` matches Keycloak realm name (`test`) | Same |
| Firebase emulator returns `connection refused` | Container binds to `127.0.0.1` inside container | Ensure `firebase.json` has `"host": "0.0.0.0"` on the `auth` emulator entry | Same |
| `FIREBASE_AUTH_EMULATOR_HOST not set` | Env var missing | `export FIREBASE_AUTH_EMULATOR_HOST=localhost:9099` | `$env:FIREBASE_AUTH_EMULATOR_HOST = "localhost:9099"` |
| Backend fails to start: `bind: address already in use` | Port 8080 taken | `export BACKEND_PORT=8081 BACKEND_URL=http://localhost:8081` | `$env:BACKEND_PORT="8081"; $env:BACKEND_URL="http://localhost:8081"` |
| `docker-compose: command not found` | Docker Desktop not installed | Install from https://docs.docker.com/get-docker/ | Same |
| PowerShell execution policy blocks `.ps1` | Default policy restricts scripts | — | `Set-ExecutionPolicy -Scope CurrentUser RemoteSigned` |
