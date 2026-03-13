# Commands Reference — Headless Test Session

All commands run during the implementation and E2E test run, in order.
Errors and retries are included so you can understand what was tried, what failed, and why.

---

## 1. Environment Setup

### Check docker compose and Go availability

```bash
# Attempt 1 — docker compose plugin (failed: not installed as plugin)
cd headless-test && docker compose version && go version

# Attempt 2 — standalone docker-compose or go (both missing)
docker-compose version 2>/dev/null || docker compose version 2>/dev/null || which docker-compose
go version

# Confirm Docker is present
which docker && docker --version
# → Docker version 25.0.14, build 0bab007

# Check for docker compose plugin locations (none found)
ls ~/.docker/cli-plugins/
ls /usr/lib/docker/cli-plugins/
ls /usr/local/lib/docker/cli-plugins/
```

### Install docker-compose (standalone binary)

```bash
# Download latest release
curl -fsSL https://github.com/docker/compose/releases/latest/download/docker-compose-linux-x86_64 \
  -o /tmp/docker-compose
chmod +x /tmp/docker-compose
/tmp/docker-compose version
# → Docker Compose version v5.1.0

# Move to PATH
sudo mv /tmp/docker-compose /usr/local/bin/docker-compose
```

### Install Go 1.21

```bash
curl -fsSL https://go.dev/dl/go1.21.13.linux-amd64.tar.gz | sudo tar -C /usr/local -xz
echo "Go installed"

# Add to PATH and verify
export PATH=$PATH:/usr/local/go/bin
go version
# → go version go1.21.13 linux/amd64
```

> **On your laptop:** Install Go from https://go.dev/dl/ and Docker Desktop from
> https://docs.docker.com/get-docker/ — you won't need these manual steps.

---

## 2. Start Docker Services

```bash
cd /home/ec2-user/workspace/my-workspace/headless-test

docker-compose up -d
# Pulls quay.io/keycloak/keycloak:24.0 and node:20-alpine, then starts both containers.
# Expected output ends with:
#   Container headless-test-keycloak-1 Started
#   Container headless-test-firebase-emulator-1 Started
```

---

## 3. Wait for Keycloak

### Attempt 1 — health/ready endpoint (failed: returns 404 on Keycloak 24 dev mode)

```bash
# This loop never exited because /health/ready always returned 404
echo "Waiting for Keycloak..." && \
  for i in $(seq 1 30); do
    if curl -sf http://localhost:8180/health/ready > /dev/null 2>&1; then
      echo "Keycloak ready after ${i}x2s"; break
    fi
    sleep 2
  done

# Confirmed 404:
curl -sf http://localhost:8180/health/ready && echo "Keycloak ready" || echo "Not ready yet"
# → Not ready yet  (exit code 22)

# Verbose check showed HTTP 404:
curl -sv http://localhost:8180/health/ready 2>&1
```

### Check container logs to confirm Keycloak actually started

```bash
docker logs headless-test-keycloak-1 2>&1 | tail -20
# → "Keycloak 24.0.5 on JVM ... started in 9.996s"
```

### Attempt 2 — /realms/master (correct indicator, returns 200)

```bash
curl -s http://localhost:8180/ | head -5
curl -s -o /dev/null -w "%{http_code}" http://localhost:8180/realms/master
# → 200
```

> **Lesson:** `/health/ready` returns 404 in Keycloak 24 dev mode.
> Use `/realms/master` as the readiness probe instead.
>
> **Recommended wait loop for your laptop:**
> ```bash
> until curl -sf http://localhost:8180/realms/master > /dev/null; do
>   echo "waiting for Keycloak..."; sleep 3
> done
> echo "Keycloak ready"
> ```

---

## 4. Configure Keycloak

```bash
bash scripts/keycloak-setup.sh
# Creates: realm 'test', client 'backend-api', role 'Backend.Call',
#          client 'simulator-client', audience mapper, assigns role to service account.
# Prints client secret at the end — copy it into .env.
```

### Write .env

```bash
cat > .env << 'EOF'
AZURE_TENANT_ID=test
AZURE_CLIENT_ID=simulator-client
AZURE_CLIENT_SECRET=<paste secret from keycloak-setup output>
BACKEND_CLIENT_ID=backend-api
TOKEN_URL=http://localhost:8180/realms/test/protocol/openid-connect/token
BACKEND_URL=http://localhost:8080
FIREBASE_AUTH_EMULATOR_HOST=localhost:9099
FIREBASE_API_KEY=local-fake-key
EOF
```

---

## 5. Go Dependencies

```bash
export PATH=$PATH:/usr/local/go/bin

# Simulator — jwt dep was removed by tidy because main.go doesn't import it
cd headless-test/simulator && go mod tidy
# → (no output, correct)

# Backend
cd headless-test/backend && go mod tidy
# → go: downloading github.com/golang-jwt/jwt/v5 v5.2.1
```

---

## 6. Unit Tests

```bash
export PATH=$PATH:/usr/local/go/bin
cd headless-test/simulator

go test ./... -v
# === RUN   TestGetToken_Success   --- PASS
# === RUN   TestGetToken_Cached    --- PASS
# === RUN   TestGetToken_Refresh   --- PASS
# === RUN   TestGetToken_Error     --- PASS
# ok   github.com/example/headless-simulator/auth  0.004s
```

---

## 7. Build and Start the Backend

### Attempt 1 — build failed (jwt/v5 API change)

```bash
export PATH=$PATH:/usr/local/go/bin
cd headless-test/backend

go build -o /tmp/headless-backend .
# ERROR: middleware/auth.go:152:19:
#   claims.Valid undefined (type jwt.MapClaims has no field or method Valid)
#
# Fix: MapClaims.Valid() was removed in jwt/v5.
#      Replaced with a manual exp claim check.
```

### Attempt 2 — build succeeded after fix

```bash
go build -o /tmp/headless-backend .
# → backend built
```

### Attempt 3 — start failed (port 8080 in use)

```bash
export PATH=$PATH:/usr/local/go/bin
export BACKEND_PORT=8081

/tmp/headless-backend &
# ERROR: listen tcp :8080: bind: address already in use
# (BACKEND_PORT wasn't set in the env because the export was in a prior shell statement)

# Check what owns port 8080:
ss -tlnp | grep 8080
lsof -i :8080 2>/dev/null | head -5
# → node process (pid 2997) — the VS Code dev server
```

### Fix — Go 1.21 mux routing + configurable port

```bash
# Also discovered: Go 1.21 net/http.ServeMux does NOT support "POST /path" patterns.
# That syntax requires Go 1.22+.
# Fixed: combined into a single /api/users handler with switch r.Method inside.
# Also added BACKEND_PORT env var support to backend/main.go.

go build -o /tmp/headless-backend .
# → built
```

### Attempt 4 — start on port 8081, stale process collision

```bash
set -a; source headless-test/.env; set +a
export BACKEND_URL=http://localhost:8081
BACKEND_PORT=8081 /tmp/headless-backend &
# ERROR: listen tcp :8081: bind: address already in use
# (previous build attempt from attempt 3 was still alive)

# Identify and kill the stale process:
ss -tlnp | grep -E '808[0-9]|809[0-9]'
# → headless-backen pid=160512 on :8081

kill 160512
```

### Attempt 5 — success

```bash
set -a; source headless-test/.env; set +a
export BACKEND_URL=http://localhost:8081

BACKEND_PORT=8081 /tmp/headless-backend &
# → 2026/03/13 02:47:25 backend listening on :8081

# Verify 401 for unauthenticated requests:
curl -s -o /dev/null -w "no-token GET: %{http_code}\n"  http://localhost:8081/api/users
curl -s -o /dev/null -w "no-token POST: %{http_code}\n" -X POST http://localhost:8081/api/users
# → no-token GET: 401
# → no-token POST: 401
```

---

## 8. Option A End-to-End Test

```bash
export PATH=$PATH:/usr/local/go/bin
set -a; source headless-test/.env; set +a
export BACKEND_URL=http://localhost:8081
unset FIREBASE_AUTH_EMULATOR_HOST   # must NOT be set for Option A

cd headless-test/simulator
go run . --option=a

# Expected output:
# [option-a] fetching token from Keycloak...  OK
#     [option-a] POST /api/users  →  201
#     [option-a] GET  /api/users  →  200
#     [option-a] no token         →  401
# All Option A assertions passed.
```

---

## 9. Firebase Emulator — Debugging

### Check emulator logs

```bash
docker logs headless-test-firebase-emulator-1 2>&1 | tail -30
# First run showed:
#   │ Authentication │ 127.0.0.1:9099 │   ← bound to container-local 127.0.0.1, NOT reachable from host
```

### Attempt 1 — probe emulator (failed: connection reset)

```bash
# Emulator bound to 127.0.0.1 inside container → port mapping didn't help
curl -s --max-time 5 "http://localhost:9099/emulator/v1/projects/local-project/config"
# → Exit code 56 (connection reset)

curl -s --max-time 5 "http://127.0.0.1:9099/emulator/v1/projects/local-project/config"
# → Exit code 56 (connection reset)
```

### Fix — add "host": "0.0.0.0" to firebase.json

```bash
# firebase.json updated to:
# { "emulators": { "auth": { "port": 9099, "host": "0.0.0.0" }, ... } }

# Restart the container to pick up the updated volume-mounted file:
cd headless-test
docker-compose restart firebase-emulator

# Wait ~15s, then confirm it now binds to 0.0.0.0:
sleep 15
docker logs headless-test-firebase-emulator-1 2>&1 | grep -E "(Auth|ready|Host:Port|0\.0\.0\.0)" | tail -10
# → │ Authentication │ 0.0.0.0:9099 │   ← now reachable from host
```

### Verify emulator is reachable

```bash
curl -s --max-time 5 "http://localhost:9099/emulator/v1/projects/local-project/config"
# → {"signIn":{"allowDuplicateEmails":false},...}
```

### Probe createCustomToken endpoint (failed: 404)

```bash
# The plan described using createCustomToken, but this endpoint doesn't exist
# in the Firebase Auth Emulator REST API.
curl -s --max-time 5 -X POST \
  "http://localhost:9099/identitytoolkit.googleapis.com/v1/projects/local-project:createCustomToken" \
  -H "Authorization: Bearer owner" \
  -H "Content-Type: application/json" \
  -d '{"uid":"test-user"}'
# → {"error":{"code":404,"message":"Not Found",...}}
```

### Discover anonymous sign-in works (returns ID token directly)

```bash
curl -s --max-time 5 -X POST \
  "http://localhost:9099/identitytoolkit.googleapis.com/v1/accounts:signUp?key=local-fake-key" \
  -H "Content-Type: application/json" \
  -d '{"returnSecureToken":true}'
# → {"kind":"...SignupNewUserResponse","localId":"...","idToken":"eyJ...","expiresIn":"3600"}
```

> **Fix applied:** `simulator/main.go` Option B flow updated to use
> `accounts:signUp` (anonymous) instead of `createCustomToken`.
> The emulator returns an ID token directly — no separate exchange step needed.

---

## 10. Option B End-to-End Test

```bash
export PATH=$PATH:/usr/local/go/bin
set -a; source headless-test/.env; set +a
export BACKEND_URL=http://localhost:8081
export FIREBASE_AUTH_EMULATOR_HOST=localhost:9099

cd headless-test/simulator
go run . --option=b

# Expected output:
# [option-b] fetching Keycloak token...        OK
# [option-b] minting Firebase custom token...  OK
# [option-b] exchanging for ID token...        OK
#     [option-b] POST /api/users  →  201
#     [option-b] GET  /api/users  →  200
#     [option-b] no token         →  401
# All Option B assertions passed.
```

---

## 11. Git — Commit and Push

```bash
cd /home/ec2-user/workspace/my-workspace

# Check existing remote (was an S3 seed bucket, not GitHub)
git remote -v
# → origin  s3+zip://developer-environment-s3bucketgit-.../my-workspace

# Update to GitHub
git remote set-url origin https://github.com/Al374/headless-test.git
git remote -v
# → origin  https://github.com/Al374/headless-test.git (fetch/push)

# Stage — exclude .env (contains secrets) and compiled binary
git add CLAUDE.md docs/ headless-test/
git restore --staged headless-test/.env headless-test/simulator/headless-simulator
git add headless-test/.gitignore

# Commit
git commit -m "Add headless-test local environment and auth design doc"

# Push
git push -u origin main
# → * [new branch]  main -> main
```

---

## Summary of Fixes Applied During the Run

| # | File | Problem | Fix |
|---|---|---|---|
| 1 | `backend/middleware/auth.go` | `MapClaims.Valid()` removed in `golang-jwt/jwt/v5` | Replaced with manual `exp` float64 check |
| 2 | `backend/main.go` | Method-based mux patterns (`"POST /path"`) require Go 1.22+ | Single `/api/users` handler with `switch r.Method` inside |
| 3 | `backend/main.go` | Port 8080 occupied by dev environment | Added `BACKEND_PORT` env var (default `"8080"`) |
| 4 | `firebase.json` | Emulator bound to `127.0.0.1` inside Docker container | Added `"host": "0.0.0.0"` to auth and ui emulator entries |
| 5 | `simulator/main.go` | `createCustomToken` REST endpoint does not exist in Firebase Auth Emulator | Replaced with `accounts:signUp` (anonymous sign-in), which returns an ID token directly |
