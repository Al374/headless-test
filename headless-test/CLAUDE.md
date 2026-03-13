# CLAUDE.md — Headless Simulator Local Test

## Project Purpose

Local test environment for the headless simulator authentication flow described in
`docs/headless-simulator-auth.md`. The goal is to validate both Option A (Azure AD
direct) and Option B (WIF + Firebase) without any real cloud accounts, using:

- **Keycloak** (Docker) — stands in for Azure Entra ID
- **Firebase Auth Emulator** (Node/Docker) — stands in for Firebase Auth
- **Go simulator** — the actual simulator code under test
- **Go backend** — minimal backend with JWT validation middleware

## Directory Structure

```
headless-test/
├── CLAUDE.md              # this file
├── README.md              # setup and run instructions
├── docker-compose.yml     # Keycloak + Firebase emulator
├── firebase.json          # emulator config
├── simulator/             # Go simulator (auth package + main)
│   ├── go.mod
│   ├── auth/
│   │   ├── azure.go       # token fetch + cache
│   │   └── azure_test.go  # unit tests
│   └── main.go            # runs the test API calls
├── backend/               # minimal Go HTTP backend
│   ├── go.mod
│   ├── middleware/
│   │   └── auth.go        # Firebase + Azure JWT validation
│   └── main.go
└── scripts/
    ├── keycloak-setup.sh   # creates realm, client, roles in Keycloak (Bash)
    └── keycloak-setup.ps1  # same, PowerShell equivalent (Windows)
```

## Current Status

- [x] Directory structure created
- [x] docker-compose.yml (Keycloak + Firebase emulator)
- [x] firebase.json
- [x] Keycloak setup script
- [x] Go simulator auth package
- [x] Go backend with middleware
- [x] Option A end-to-end test passing
- [x] Option B end-to-end test passing

## Key Decisions

- Keycloak runs on port **8180** (8080 is reserved for the backend)
- Firebase Auth Emulator runs on port **9099**, UI on **4000**
- Backend runs on port **8080**
- Simulator is a short-lived Go binary — runs, hits all endpoints, exits 0 on success
- Both Option A (Azure AD direct) and Option B (Firebase token) are tested
- `FIREBASE_AUTH_EMULATOR_HOST=localhost:9099` is set for both emulator and backend

## Environment Variables

### Option A (Azure AD direct)

| Variable | Local value |
|---|---|
| `AZURE_TENANT_ID` | `test` (Keycloak realm name) |
| `AZURE_CLIENT_ID` | `simulator-client` |
| `AZURE_CLIENT_SECRET` | set by keycloak-setup script |
| `BACKEND_CLIENT_ID` | `backend-api` |
| `TOKEN_URL` | `http://localhost:8180/realms/test/protocol/openid-connect/token` |
| `BACKEND_URL` | `http://localhost:8080` |

Load from `.env` file:

```bash
# Bash / macOS / Linux
source .env
```

```powershell
# PowerShell (Windows)
Get-Content .env | ForEach-Object {
    if ($_ -match '^\s*([^#][^=]+)=(.*)$') {
        [System.Environment]::SetEnvironmentVariable($matches[1].Trim(), $matches[2].Trim(), 'Process')
    }
}
```

Set a single variable inline:

```bash
# Bash / macOS / Linux
export FIREBASE_AUTH_EMULATOR_HOST=localhost:9099
```

```powershell
# PowerShell (Windows)
$env:FIREBASE_AUTH_EMULATOR_HOST = "localhost:9099"
```

### Option B (Firebase token)

| Variable | Local value |
|---|---|
| All Option A vars | same as above |
| `FIREBASE_AUTH_EMULATOR_HOST` | `localhost:9099` |
| `FIREBASE_API_KEY` | `local-fake-key` (emulator accepts any value) |

## Shell Command Reference

| Action | Bash / macOS / Linux | PowerShell (Windows) |
|---|---|---|
| Start services | `docker-compose up -d` | `docker-compose up -d` |
| Watch logs | `docker-compose logs -f keycloak \| grep "started"` | `docker-compose logs -f keycloak \| Select-String "started"` |
| Run setup script | `bash scripts/keycloak-setup.sh` | `.\scripts\keycloak-setup.ps1` |
| Copy file | `cp .env.example .env` | `Copy-Item .env.example .env` |
| Load env file | `source .env` | `Get-Content .env \| ForEach-Object { ... }` (see above) |
| Set env var | `export KEY=value` | `$env:KEY = "value"` |
| Run Go binary | `go run . --option=a` | `go run . --option=a` |
| Background process | `go run . &` | `$job = Start-Job { go run . }` |
| Stop background | `kill %1` | `Stop-Job $job; Remove-Job $job` |
| Run tests | `go test ./...` | `go test ./...` |
| Stop services | `docker-compose down` | `docker-compose down` |
| Allow PS scripts | _(n/a)_ | `Set-ExecutionPolicy -Scope CurrentUser RemoteSigned` |

## Reference

Full auth design: `../docs/headless-simulator-auth.md`
