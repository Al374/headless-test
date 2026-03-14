package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/example/headless-simulator-wif/apigee"
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

// doRequest sends an HTTP request with an optional Bearer token and extra headers.
// Pass nil for extraHeaders when not needed.
func doRequest(ctx context.Context, method, endpoint, token string, body []byte, extraHeaders map[string]string) *http.Response {
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
	for k, v := range extraHeaders {
		req.Header.Set(k, v)
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

// decodeIDTokenClaims base64-decodes the payload section of a JWT and returns
// the claims as a map. Used only for logging — not a security-sensitive path.
func decodeIDTokenClaims(idToken string) (map[string]interface{}, error) {
	parts := strings.Split(idToken, ".")
	if len(parts) != 3 {
		return nil, fmt.Errorf("not a JWT")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, err
	}
	var claims map[string]interface{}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil, err
	}
	return claims, nil
}

func main() {
	viaApigee := flag.Bool("via-apigee", false,
		"route API calls through Apigee: sends Apigee token as Bearer and Firebase JWT as X-FB-TOKEN")
	flag.Parse()

	ctx := context.Background()
	backendURL := mustEnv("BACKEND_URL")

	// ── Optional: custom claims to embed in the Firebase token ─────────────────
	var customClaims map[string]interface{}
	if raw := os.Getenv("FIREBASE_CUSTOM_CLAIMS"); raw != "" {
		if err := json.Unmarshal([]byte(raw), &customClaims); err != nil {
			log.Fatalf("FIREBASE_CUSTOM_CLAIMS is not valid JSON: %v", err)
		}
	}

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
		CustomClaims:        customClaims,
	}

	fmt.Print("[option-b] WIF STS exchange...               ")
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
	if len(customClaims) > 0 {
		b, _ := json.Marshal(customClaims)
		fmt.Printf("OK\n    claims injected: %s\n", b)
	} else {
		fmt.Println("OK")
	}

	fmt.Print("[option-b] exchanging for Firebase ID token  ")
	idToken, err := wif.SignInWithCustomToken(ctx, cfg, customToken)
	if err != nil {
		log.Fatalf("%v", err)
	}
	fmt.Println("OK")
	if idClaims, err := decodeIDTokenClaims(idToken); err == nil {
		// Print only the application-level claims (skip standard JWT fields).
		skip := map[string]bool{
			"iss": true, "aud": true, "auth_time": true,
			"sub": true, "iat": true, "exp": true,
			"firebase": true, "user_id": true,
		}
		appClaims := map[string]interface{}{}
		for k, v := range idClaims {
			if !skip[k] {
				appClaims[k] = v
			}
		}
		if len(appClaims) > 0 {
			b, _ := json.Marshal(appClaims)
			fmt.Printf("    id token claims: %s\n", b)
		}
	}

	// ── Apigee mode: fetch Apigee token, route requests through Apigee ────────
	// bearerToken is what goes in Authorization: Bearer.
	// extraHeaders carries X-FB-TOKEN when routing through Apigee.
	bearerToken := idToken
	var extraHeaders map[string]string

	if *viaApigee {
		fmt.Print("[option-b] fetching Apigee token...          ")
		apigeeToken, err := apigee.FetchToken(ctx, apigee.Config{
			TokenURL:     mustEnv("APIGEE_TOKEN_URL"),
			ClientID:     mustEnv("APIGEE_CLIENT_ID"),
			ClientSecret: mustEnv("APIGEE_CLIENT_SECRET"),
			Scope:        os.Getenv("APIGEE_SCOPE"),       // optional
			AuthScheme:   os.Getenv("APIGEE_AUTH_SCHEME"), // "basic" (default) or "body"
		})
		if err != nil {
			log.Fatalf("apigee: %v", err)
		}
		fmt.Println("OK")
		fmt.Println("    route: Authorization=Apigee token  X-FB-TOKEN=Firebase JWT")

		// Send Apigee token as Bearer; Firebase JWT travels in X-FB-TOKEN.
		// Apigee validates the Bearer token, then replaces it with X-FB-TOKEN
		// before forwarding to the backend — matching the production flow.
		bearerToken = apigeeToken
		extraHeaders = map[string]string{"X-FB-TOKEN": idToken}
	}

	// ── API assertions ─────────────────────────────────────────────────────────
	apiPayload, _ := json.Marshal(map[string]string{"name": "wif-test-user"})

	resp := doRequest(ctx, http.MethodPost, backendURL+"/api/users", bearerToken, apiPayload, extraHeaders)
	assertStatus(resp, http.StatusCreated, "[option-b] POST /api/users")

	resp = doRequest(ctx, http.MethodGet, backendURL+"/api/users", bearerToken, nil, extraHeaders)
	assertStatus(resp, http.StatusOK, "[option-b] GET  /api/users")

	resp = doRequest(ctx, http.MethodPost, backendURL+"/api/users", "", apiPayload, nil)
	assertStatus(resp, http.StatusUnauthorized, "[option-b] no token        ")

	fmt.Println("All Option B assertions passed.")
}
