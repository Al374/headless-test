package apigee

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFetchToken_BasicScheme(t *testing.T) {
	var gotAuthHeader string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuthHeader = r.Header.Get("Authorization")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"access_token": "apigee-token-basic",
			"token_type":   "Bearer",
			"expires_in":   1799,
		})
	}))
	defer srv.Close()

	token, err := FetchToken(context.Background(), Config{
		ClientID:     "test-client",
		ClientSecret: "test-secret",
		AuthScheme:   "basic",
		OverrideURL:  srv.URL,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if token != "apigee-token-basic" {
		t.Errorf("got token %q, want %q", token, "apigee-token-basic")
	}

	// Verify Basic auth header was sent.
	expected := "Basic " + base64.StdEncoding.EncodeToString([]byte("test-client:test-secret"))
	if gotAuthHeader != expected {
		t.Errorf("Authorization header = %q, want %q", gotAuthHeader, expected)
	}
}

func TestFetchToken_BodyScheme(t *testing.T) {
	var gotClientID string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		gotClientID = r.FormValue("client_id")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"access_token": "apigee-token-body",
			"token_type":   "Bearer",
			"expires_in":   1799,
		})
	}))
	defer srv.Close()

	token, err := FetchToken(context.Background(), Config{
		ClientID:     "body-client",
		ClientSecret: "body-secret",
		AuthScheme:   "body",
		OverrideURL:  srv.URL,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if token != "apigee-token-body" {
		t.Errorf("got token %q, want %q", token, "apigee-token-body")
	}
	if gotClientID != "body-client" {
		t.Errorf("client_id in body = %q, want %q", gotClientID, "body-client")
	}
}

func TestFetchToken_ErrorResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error":             "invalid_client",
			"error_description": "client authentication failed",
		})
	}))
	defer srv.Close()

	_, err := FetchToken(context.Background(), Config{
		ClientID:    "bad-client",
		ClientSecret: "bad-secret",
		OverrideURL: srv.URL,
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "invalid_client") {
		t.Errorf("error %q does not mention invalid_client", err.Error())
	}
}

func TestFetchToken_MissingURL(t *testing.T) {
	_, err := FetchToken(context.Background(), Config{
		ClientID:     "x",
		ClientSecret: "y",
	})
	if err == nil {
		t.Fatal("expected error for missing TokenURL, got nil")
	}
}
