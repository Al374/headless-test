package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/example/headless-simulator/auth"
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
		log.Fatalf("expected %d for %s, got %d", want, label, resp.StatusCode)
	}
}

func doRequest(method, url, token string, body []byte) *http.Response {
	var reqBody io.Reader
	if body != nil {
		reqBody = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, url, reqBody)
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
		log.Fatalf("%s %s: %v", method, url, err)
	}
	return resp
}

func runOptionA(backendURL string, provider *auth.AzureTokenProvider) {
	fmt.Print("[option-a] fetching token from Keycloak...  ")
	token, err := provider.GetToken(context.Background())
	if err != nil {
		log.Fatalf("GetToken: %v", err)
	}
	fmt.Println("OK")

	payload, _ := json.Marshal(map[string]string{"name": "test-user"})

	resp := doRequest(http.MethodPost, backendURL+"/api/users", token, payload)
	assertStatus(resp, http.StatusCreated, "[option-a] POST /api/users")

	resp = doRequest(http.MethodGet, backendURL+"/api/users", token, nil)
	assertStatus(resp, http.StatusOK, "[option-a] GET  /api/users")

	resp = doRequest(http.MethodPost, backendURL+"/api/users", "", payload)
	assertStatus(resp, http.StatusUnauthorized, "[option-a] no token        ")

	fmt.Println("All Option A assertions passed.")
}

func runOptionB(backendURL string, provider *auth.AzureTokenProvider) {
	emulatorHost := os.Getenv("FIREBASE_AUTH_EMULATOR_HOST")
	if emulatorHost == "" {
		log.Fatal("FIREBASE_AUTH_EMULATOR_HOST must be set for Option B")
	}
	if !strings.HasPrefix(emulatorHost, "http") {
		emulatorHost = "http://" + emulatorHost
	}
	apiKey := os.Getenv("FIREBASE_API_KEY")
	if apiKey == "" {
		apiKey = "local-fake-key"
	}

	// Step 1: fetch Keycloak token (same as Option A)
	fmt.Print("[option-b] fetching Keycloak token...        ")
	_, err := provider.GetToken(context.Background())
	if err != nil {
		log.Fatalf("GetToken: %v", err)
	}
	fmt.Println("OK")

	// Step 2: sign up anonymously on the emulator to get a user record.
	// The emulator does not expose a createCustomToken REST endpoint, so we
	// use anonymous sign-up (accounts:signUp with no credentials) as the
	// "custom token minting" step — it is equivalent for local testing.
	fmt.Print("[option-b] minting Firebase custom token...  ")
	signUpURL := emulatorHost + "/identitytoolkit.googleapis.com/v1/accounts:signUp?key=" + apiKey
	signUpPayload, _ := json.Marshal(map[string]interface{}{"returnSecureToken": true})
	signUpReq, _ := http.NewRequestWithContext(context.Background(),
		http.MethodPost, signUpURL, bytes.NewReader(signUpPayload))
	signUpReq.Header.Set("Content-Type", "application/json")

	signUpResp, err := http.DefaultClient.Do(signUpReq)
	if err != nil {
		log.Fatalf("anonymous sign-up: %v", err)
	}
	defer signUpResp.Body.Close()
	var signUpBody struct {
		LocalID      string `json:"localId"`
		IDToken      string `json:"idToken"`
		RefreshToken string `json:"refreshToken"`
	}
	if err := json.NewDecoder(signUpResp.Body).Decode(&signUpBody); err != nil {
		log.Fatalf("decode sign-up response: %v", err)
	}
	if signUpBody.LocalID == "" {
		log.Fatal("empty localId from emulator sign-up")
	}
	fmt.Println("OK")

	// Step 3: the ID token is returned directly by accounts:signUp in emulator
	// mode, so "exchanging for ID token" is a no-op confirmation step.
	fmt.Print("[option-b] exchanging for ID token...        ")
	if signUpBody.IDToken == "" {
		log.Fatal("empty idToken from Firebase emulator sign-up")
	}
	fmt.Println("OK")

	idToken := signUpBody.IDToken

	// Step 4: same 3 API calls as Option A.
	apiPayload, _ := json.Marshal(map[string]string{"name": "test-user"})

	resp := doRequest(http.MethodPost, backendURL+"/api/users", idToken, apiPayload)
	assertStatus(resp, http.StatusCreated, "[option-b] POST /api/users")

	resp = doRequest(http.MethodGet, backendURL+"/api/users", idToken, nil)
	assertStatus(resp, http.StatusOK, "[option-b] GET  /api/users")

	resp = doRequest(http.MethodPost, backendURL+"/api/users", "", apiPayload)
	assertStatus(resp, http.StatusUnauthorized, "[option-b] no token        ")

	fmt.Println("All Option B assertions passed.")
}

func main() {
	option := flag.String("option", "a", "auth option: a or b")
	flag.Parse()

	tokenURL := mustEnv("TOKEN_URL")
	clientID := mustEnv("AZURE_CLIENT_ID")
	clientSecret := mustEnv("AZURE_CLIENT_SECRET")
	audience := mustEnv("BACKEND_CLIENT_ID")
	backendURL := mustEnv("BACKEND_URL")

	provider := auth.NewAzureTokenProvider(auth.AzureConfig{
		TokenURL:     tokenURL,
		ClientID:     clientID,
		ClientSecret: clientSecret,
		Audience:     audience,
	})

	switch *option {
	case "a":
		runOptionA(backendURL, provider)
	case "b":
		runOptionB(backendURL, provider)
	default:
		log.Fatalf("unknown option %q; use --option=a or --option=b", *option)
	}
}
