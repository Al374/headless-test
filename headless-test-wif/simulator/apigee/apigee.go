// Package apigee fetches an OAuth 2.0 access token from an Apigee auth endpoint
// using the client_credentials grant.
//
// Apigee Edge commonly expects the client credentials in a Basic Authorization
// header rather than the request body.  Set AuthScheme="body" if your Apigee
// deployment accepts standard form-encoded credentials instead.
package apigee

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// Config holds the parameters needed to obtain an Apigee access token.
// All values come from environment variables — nothing is hardcoded.
type Config struct {
	TokenURL     string // Apigee OAuth token endpoint (AUTH_ENDPOINT)
	ClientID     string
	ClientSecret string
	Scope        string // optional; leave empty if your Apigee proxy does not require it
	AuthScheme   string // "basic" (default) or "body"

	// OverrideURL replaces TokenURL in unit tests.
	OverrideURL string
}

// FetchToken performs a client_credentials grant against the Apigee auth
// endpoint and returns the access token string.
func FetchToken(ctx context.Context, cfg Config) (string, error) {
	tokenURL := cfg.TokenURL
	if cfg.OverrideURL != "" {
		tokenURL = cfg.OverrideURL
	}
	if tokenURL == "" {
		return "", fmt.Errorf("apigee: TokenURL is required")
	}

	form := url.Values{}
	form.Set("grant_type", "client_credentials")
	if cfg.Scope != "" {
		form.Set("scope", cfg.Scope)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL,
		strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	// Apigee Edge default: credentials in Basic Authorization header.
	// Set AuthScheme="body" for deployments that accept form-encoded creds.
	scheme := cfg.AuthScheme
	if scheme == "" {
		scheme = "basic"
	}
	switch scheme {
	case "basic":
		creds := base64.StdEncoding.EncodeToString(
			[]byte(cfg.ClientID + ":" + cfg.ClientSecret))
		req.Header.Set("Authorization", "Basic "+creds)
	case "body":
		// Re-encode body including client credentials.
		form.Set("client_id", cfg.ClientID)
		form.Set("client_secret", cfg.ClientSecret)
		req, err = http.NewRequestWithContext(ctx, http.MethodPost, tokenURL,
			strings.NewReader(form.Encode()))
		if err != nil {
			return "", err
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	default:
		return "", fmt.Errorf("apigee: unknown AuthScheme %q (use \"basic\" or \"body\")", scheme)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("apigee token request: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("reading apigee token response: %w", err)
	}

	var tr struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
		ErrorDesc   string `json:"error_description"`
	}
	if err := json.Unmarshal(body, &tr); err != nil {
		return "", fmt.Errorf("parsing apigee token response (status %d): %w\nbody: %s",
			resp.StatusCode, err, body)
	}
	if tr.Error != "" {
		return "", fmt.Errorf("apigee token error %s: %s", tr.Error, tr.ErrorDesc)
	}
	if tr.AccessToken == "" {
		return "", fmt.Errorf("empty access_token from apigee (status %d)", resp.StatusCode)
	}
	return tr.AccessToken, nil
}
