package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func makeTokenServer(t *testing.T, status int, body map[string]interface{}) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		json.NewEncoder(w).Encode(body)
	}))
}

func TestGetToken_Success(t *testing.T) {
	srv := makeTokenServer(t, http.StatusOK, map[string]interface{}{
		"access_token": "my-access-token",
		"expires_in":   3600,
	})
	defer srv.Close()

	p := NewAzureTokenProvider(AzureConfig{
		TokenURL:     srv.URL,
		ClientID:     "client",
		ClientSecret: "secret",
		Audience:     "backend-api",
	})

	tok, err := p.GetToken(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tok != "my-access-token" {
		t.Errorf("got token %q, want %q", tok, "my-access-token")
	}
}

func TestGetToken_Cached(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"access_token": "cached-token",
			"expires_in":   3600,
		})
	}))
	defer srv.Close()

	p := NewAzureTokenProvider(AzureConfig{TokenURL: srv.URL})

	if _, err := p.GetToken(context.Background()); err != nil {
		t.Fatalf("first call error: %v", err)
	}
	if _, err := p.GetToken(context.Background()); err != nil {
		t.Fatalf("second call error: %v", err)
	}

	if calls != 1 {
		t.Errorf("token endpoint called %d times, want 1", calls)
	}
}

func TestGetToken_Refresh(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"access_token": "fresh-token",
			"expires_in":   3600,
		})
	}))
	defer srv.Close()

	p := NewAzureTokenProvider(AzureConfig{TokenURL: srv.URL})
	// Pre-load cache with an already-expired token.
	p.cached = "stale-token"
	p.expiry = time.Now().Add(-1 * time.Second) // already expired

	tok, err := p.GetToken(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tok != "fresh-token" {
		t.Errorf("got %q, want %q", tok, "fresh-token")
	}
	if calls != 1 {
		t.Errorf("token endpoint called %d times, want 1", calls)
	}
}

func TestGetToken_Error(t *testing.T) {
	srv := makeTokenServer(t, http.StatusUnauthorized, map[string]interface{}{
		"error":             "unauthorized_client",
		"error_description": "bad credentials",
	})
	defer srv.Close()

	p := NewAzureTokenProvider(AzureConfig{TokenURL: srv.URL})

	_, err := p.GetToken(context.Background())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}
