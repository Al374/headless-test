// Package wif implements the Azure → GCP WIF → Firebase ID token exchange chain.
//
// Full flow:
//   1. Azure access token  (fetched by caller using client credentials)
//   2. GCP federated token (WIF STS token exchange)
//   3. SA access token     (service account impersonation via IAM Credentials API)
//   4. Firebase custom JWT (signed by SA via IAM signJwt API — no key file needed)
//   5. Firebase ID token   (exchanged via Identity Toolkit signInWithCustomToken)
package wif

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Config holds every parameter needed for the WIF chain.
// All values come from environment variables — nothing is hardcoded.
// The Override* fields are empty in production and set by unit tests.
type Config struct {
	// Azure access token — fetched by the caller before invoking the chain.
	AzureToken string

	// GCP Workload Identity Federation
	ProjectNumber string // e.g. "123456789012"
	PoolID        string // e.g. "azure-pool"
	ProviderID    string // e.g. "azure-provider"

	// Service Account to impersonate
	ServiceAccountEmail string // e.g. "simulator-sa@project.iam.gserviceaccount.com"

	// Firebase
	ProjectID    string                 // GCP project ID == Firebase project ID
	APIKey       string                 // Firebase Web API Key (from Firebase console)
	UID          string                 // uid embedded in the Firebase custom token (any non-empty string)
	CustomClaims map[string]interface{} // optional; propagated into the Firebase ID token as top-level claims

	// Override base URLs — leave empty in production; set in unit tests.
	OverrideSTSURL string // replaces https://sts.googleapis.com
	OverrideIAMURL string // replaces https://iamcredentials.googleapis.com
	OverrideIDTURL string // replaces https://identitytoolkit.googleapis.com
}

// ExchangeForFirebaseIDToken runs all four steps and returns a signed Firebase ID token.
func ExchangeForFirebaseIDToken(ctx context.Context, cfg Config) (string, error) {
	fedToken, err := STSExchange(ctx, cfg)
	if err != nil {
		return "", fmt.Errorf("step 1 WIF STS exchange: %w", err)
	}

	saToken, err := GenerateSAAccessToken(ctx, cfg, fedToken)
	if err != nil {
		return "", fmt.Errorf("step 2 SA impersonation: %w", err)
	}

	customToken, err := SignFirebaseCustomToken(ctx, cfg, saToken)
	if err != nil {
		return "", fmt.Errorf("step 3 sign custom token: %w", err)
	}

	idToken, err := SignInWithCustomToken(ctx, cfg, customToken)
	if err != nil {
		return "", fmt.Errorf("step 4 sign in with custom token: %w", err)
	}

	return idToken, nil
}

// stsExchange posts to the GCP STS token exchange endpoint and returns a
// short-lived federated access token scoped to the WIF pool.
func STSExchange(ctx context.Context, cfg Config) (string, error) {
	base := cfg.OverrideSTSURL
	if base == "" {
		base = "https://sts.googleapis.com"
	}
	audience := fmt.Sprintf(
		"//iam.googleapis.com/projects/%s/locations/global/workloadIdentityPools/%s/providers/%s",
		cfg.ProjectNumber, cfg.PoolID, cfg.ProviderID,
	)

	form := url.Values{}
	form.Set("grant_type", "urn:ietf:params:oauth:grant-type:token-exchange")
	form.Set("audience", audience)
	form.Set("subject_token_type", "urn:ietf:params:oauth:token-type:jwt")
	form.Set("subject_token", cfg.AzureToken)
	form.Set("requested_token_type", "urn:ietf:params:oauth:token-type:access_token")
	form.Set("scope", "https://www.googleapis.com/auth/cloud-platform")

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		base+"/v1/token", strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	var resp struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
		ErrorDesc   string `json:"error_description"`
	}
	if err := doJSON(req, &resp); err != nil {
		return "", err
	}
	if resp.Error != "" {
		return "", fmt.Errorf("%s: %s", resp.Error, resp.ErrorDesc)
	}
	if resp.AccessToken == "" {
		return "", fmt.Errorf("empty access_token from STS")
	}
	return resp.AccessToken, nil
}

// generateSAAccessToken calls the IAM Credentials API to impersonate the
// configured service account using the WIF federated token.
func GenerateSAAccessToken(ctx context.Context, cfg Config, federatedToken string) (string, error) {
	base := cfg.OverrideIAMURL
	if base == "" {
		base = "https://iamcredentials.googleapis.com"
	}
	endpoint := fmt.Sprintf(
		"%s/v1/projects/-/serviceAccounts/%s:generateAccessToken",
		base, cfg.ServiceAccountEmail,
	)

	body, _ := json.Marshal(map[string]interface{}{
		"scope": []string{"https://www.googleapis.com/auth/cloud-platform"},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+federatedToken)

	var resp struct {
		AccessToken string `json:"accessToken"`
		Error       *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := doJSON(req, &resp); err != nil {
		return "", err
	}
	if resp.Error != nil {
		return "", fmt.Errorf("generateAccessToken: %s", resp.Error.Message)
	}
	if resp.AccessToken == "" {
		return "", fmt.Errorf("empty accessToken from SA impersonation")
	}
	return resp.AccessToken, nil
}

// signFirebaseCustomToken asks the IAM signJwt API to sign a Firebase custom
// token payload using the service account's key. No private key file is needed.
func SignFirebaseCustomToken(ctx context.Context, cfg Config, saToken string) (string, error) {
	base := cfg.OverrideIAMURL
	if base == "" {
		base = "https://iamcredentials.googleapis.com"
	}
	endpoint := fmt.Sprintf(
		"%s/v1/projects/-/serviceAccounts/%s:signJwt",
		base, cfg.ServiceAccountEmail,
	)

	uid := cfg.UID
	if uid == "" {
		uid = "wif-simulator-user"
	}
	now := time.Now().Unix()

	claims := cfg.CustomClaims
	if claims == nil {
		claims = map[string]interface{}{}
	}

	// Firebase custom token payload — must be a JSON string inside the outer request.
	payload, err := json.Marshal(map[string]interface{}{
		"iss":    cfg.ServiceAccountEmail,
		"sub":    cfg.ServiceAccountEmail,
		"aud":    "https://identitytoolkit.googleapis.com/google.identity.identitytoolkit.v1.IdentityToolkit",
		"iat":    now,
		"exp":    now + 3600,
		"uid":    uid,
		"claims": claims,
	})
	if err != nil {
		return "", err
	}

	body, _ := json.Marshal(map[string]string{"payload": string(payload)})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+saToken)

	var resp struct {
		SignedJwt string `json:"signedJwt"`
		Error     *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := doJSON(req, &resp); err != nil {
		return "", err
	}
	if resp.Error != nil {
		return "", fmt.Errorf("signJwt: %s", resp.Error.Message)
	}
	if resp.SignedJwt == "" {
		return "", fmt.Errorf("empty signedJwt from IAM")
	}
	return resp.SignedJwt, nil
}

// signInWithCustomToken exchanges a Firebase custom token for a Firebase ID token
// via the Identity Toolkit API.
func SignInWithCustomToken(ctx context.Context, cfg Config, customToken string) (string, error) {
	base := cfg.OverrideIDTURL
	if base == "" {
		base = "https://identitytoolkit.googleapis.com"
	}
	endpoint := fmt.Sprintf(
		"%s/v1/accounts:signInWithCustomToken?key=%s",
		base, cfg.APIKey,
	)

	body, _ := json.Marshal(map[string]interface{}{
		"token":             customToken,
		"returnSecureToken": true,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	var resp struct {
		IDToken string `json:"idToken"`
		Error   *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := doJSON(req, &resp); err != nil {
		return "", err
	}
	if resp.Error != nil {
		return "", fmt.Errorf("signInWithCustomToken: %s", resp.Error.Message)
	}
	if resp.IDToken == "" {
		return "", fmt.Errorf("empty idToken from Firebase")
	}
	return resp.IDToken, nil
}

// doJSON executes the request and JSON-decodes the response body into v.
func doJSON(req *http.Request, v interface{}) error {
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", req.Method, req.URL, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("reading response body: %w", err)
	}
	if err := json.Unmarshal(body, v); err != nil {
		return fmt.Errorf("parsing response from %s (status %d): %w\nbody: %s",
			req.URL, resp.StatusCode, err, body)
	}
	return nil
}
