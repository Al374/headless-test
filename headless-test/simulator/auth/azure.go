package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// AzureConfig holds the token endpoint and client credentials.
type AzureConfig struct {
	TokenURL     string // e.g. http://localhost:8180/realms/test/protocol/openid-connect/token
	ClientID     string
	ClientSecret string
	Audience     string // backend-api
}

// AzureTokenProvider fetches and caches a client_credentials token.
type AzureTokenProvider struct {
	cfg    AzureConfig
	mu     sync.Mutex
	cached string
	expiry time.Time
}

// NewAzureTokenProvider constructs a provider from cfg.
func NewAzureTokenProvider(cfg AzureConfig) *AzureTokenProvider {
	return &AzureTokenProvider{cfg: cfg}
}

type tokenResponse struct {
	AccessToken string `json:"access_token"`
	ExpiresIn   int    `json:"expires_in"`
	Error       string `json:"error"`
	ErrorDesc   string `json:"error_description"`
}

// GetToken returns a valid token, fetching a new one when the cached token is
// absent or within 60 seconds of expiry.
func (p *AzureTokenProvider) GetToken(ctx context.Context) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.cached != "" && time.Now().Before(p.expiry.Add(-60*time.Second)) {
		return p.cached, nil
	}

	token, expiresIn, err := p.fetchToken(ctx)
	if err != nil {
		return "", err
	}

	p.cached = token
	p.expiry = time.Now().Add(time.Duration(expiresIn) * time.Second)
	return p.cached, nil
}

func (p *AzureTokenProvider) fetchToken(ctx context.Context) (string, int, error) {
	form := url.Values{}
	form.Set("grant_type", "client_credentials")
	form.Set("client_id", p.cfg.ClientID)
	form.Set("client_secret", p.cfg.ClientSecret)
	// Azure AD requires "<audience>/.default" as the scope for client credentials.
	// Keycloak uses plain "openid". Auto-detect based on the token URL.
	scope := "openid"
	if strings.Contains(p.cfg.TokenURL, "microsoftonline.com") {
		scope = p.cfg.Audience + "/.default"
	}
	form.Set("scope", scope)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.cfg.TokenURL,
		strings.NewReader(form.Encode()))
	if err != nil {
		return "", 0, fmt.Errorf("building token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", 0, fmt.Errorf("token request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", 0, fmt.Errorf("reading token response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", 0, fmt.Errorf("token endpoint returned %d: %s", resp.StatusCode, body)
	}

	var tr tokenResponse
	if err := json.Unmarshal(body, &tr); err != nil {
		return "", 0, fmt.Errorf("parsing token response: %w", err)
	}
	if tr.Error != "" {
		return "", 0, fmt.Errorf("token error %s: %s", tr.Error, tr.ErrorDesc)
	}
	if tr.AccessToken == "" {
		return "", 0, fmt.Errorf("empty access_token in response")
	}

	return tr.AccessToken, tr.ExpiresIn, nil
}
