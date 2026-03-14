package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/example/headless-simulator-wif/wif"
)

func mustEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		log.Fatalf("required env var %s is not set", key)
	}
	return v
}

func assertStatus(resp *http.Response, want int, label string) {
	fmt.Printf("    %s  →  %d\n", label, resp.StatusCode)
	if resp.StatusCode != want {
		body, _ := io.ReadAll(resp.Body)
		log.Fatalf("expected %d for %s, got %d\nbody: %s", want, label, resp.StatusCode, body)
	}
}

func doRequest(ctx context.Context, method, endpoint, token string, body []byte) *http.Response {
	var reqBody io.Reader
	if body != nil {
		reqBody = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, reqBody)
	if err != nil {
		log.Fatalf("building request: %v", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Fatalf("%s %s: %v", method, endpoint, err)
	}
	return resp
}

// fetchAzureToken performs a client_credentials grant and returns an access token.
func fetchAzureToken(ctx context.Context) (string, error) {
	tokenURL := mustEnv("TOKEN_URL")
	clientID := mustEnv("AZURE_CLIENT_ID")
	clientSecret := mustEnv("AZURE_CLIENT_SECRET")

	form := url.Values{}
	form.Set("grant_type", "client_credentials")
	form.Set("client_id", clientID)
	form.Set("client_secret", clientSecret)
	// For Azure AD client credentials: scope = "<client_id>/.default"
	form.Set("scope", clientID+"/.default")

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL,
		strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("token request: %w", err)
	}
	defer resp.Body.Close()

	var tr struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
		ErrorDesc   string `json:"error_description"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tr); err != nil {
		return "", fmt.Errorf("decode token response: %w", err)
	}
	if tr.Error != "" {
		return "", fmt.Errorf("token error %s: %s", tr.Error, tr.ErrorDesc)
	}
	if tr.AccessToken == "" {
		return "", fmt.Errorf("empty access_token")
	}
	return tr.AccessToken, nil
}

func main() {
	ctx := context.Background()
	backendURL := mustEnv("BACKEND_URL")

	// ── Step 1: Azure token ────────────────────────────────────────────────────
	fmt.Print("[option-b] fetching Azure token...           ")
	azureToken, err := fetchAzureToken(ctx)
	if err != nil {
		log.Fatalf("fetchAzureToken: %v", err)
	}
	fmt.Println("OK")

	// ── Steps 2–5: WIF chain → Firebase ID token ──────────────────────────────
	cfg := wif.Config{
		AzureToken:          azureToken,
		ProjectNumber:       mustEnv("GCP_PROJECT_NUMBER"),
		PoolID:              mustEnv("GCP_WIF_POOL"),
		ProviderID:          mustEnv("GCP_WIF_PROVIDER"),
		ServiceAccountEmail: mustEnv("GCP_SERVICE_ACCOUNT"),
		ProjectID:           mustEnv("GCP_PROJECT_ID"),
		APIKey:              mustEnv("FIREBASE_API_KEY"),
		UID:                 "wif-simulator-user",
	}

	fmt.Print("[option-b] WIF STS exchange...               ")
	// Run step-by-step so we can log each phase individually.
	fedToken, err := wif.STSExchange(ctx, cfg)
	if err != nil {
		log.Fatalf("%v", err)
	}
	fmt.Println("OK")

	fmt.Print("[option-b] SA impersonation...               ")
	saToken, err := wif.GenerateSAAccessToken(ctx, cfg, fedToken)
	if err != nil {
		log.Fatalf("%v", err)
	}
	fmt.Println("OK")

	fmt.Print("[option-b] minting Firebase custom token...  ")
	customToken, err := wif.SignFirebaseCustomToken(ctx, cfg, saToken)
	if err != nil {
		log.Fatalf("%v", err)
	}
	fmt.Println("OK")

	fmt.Print("[option-b] exchanging for Firebase ID token  ")
	idToken, err := wif.SignInWithCustomToken(ctx, cfg, customToken)
	if err != nil {
		log.Fatalf("%v", err)
	}
	fmt.Println("OK")

	// ── API assertions ─────────────────────────────────────────────────────────
	payload, _ := json.Marshal(map[string]string{"name": "wif-test-user"})

	resp := doRequest(ctx, http.MethodPost, backendURL+"/api/users", idToken, payload)
	assertStatus(resp, http.StatusCreated, "[option-b] POST /api/users")

	resp = doRequest(ctx, http.MethodGet, backendURL+"/api/users", idToken, nil)
	assertStatus(resp, http.StatusOK, "[option-b] GET  /api/users")

	resp = doRequest(ctx, http.MethodPost, backendURL+"/api/users", "", payload)
	assertStatus(resp, http.StatusUnauthorized, "[option-b] no token        ")

	fmt.Println("All Option B assertions passed.")
}
