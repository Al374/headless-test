package wif

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newTestServers spins up three httptest servers that mock:
//
//	stsServer  — GCP STS token exchange
//	iamServer  — IAM Credentials API (generateAccessToken + signJwt on the same server)
//	idtServer  — Identity Toolkit (signInWithCustomToken)
//
// Each server records how many times it was called so tests can assert call counts.
func newMockSTS(t *testing.T, accessToken string, statusCode int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/token" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(statusCode)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"access_token": accessToken,
			"token_type":   "Bearer",
			"expires_in":   3600,
		})
	}))
}

func newMockIAM(t *testing.T, saToken, signedJwt string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, ":generateAccessToken"):
			json.NewEncoder(w).Encode(map[string]interface{}{
				"accessToken": saToken,
				"expireTime":  "2099-01-01T00:00:00Z",
			})
		case strings.HasSuffix(r.URL.Path, ":signJwt"):
			json.NewEncoder(w).Encode(map[string]interface{}{
				"signedJwt": signedJwt,
				"keyId":     "test-key-id",
			})
		default:
			http.NotFound(w, r)
		}
	}))
}

func newMockIDT(t *testing.T, idToken string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"idToken":      idToken,
			"refreshToken": "refresh-token",
			"expiresIn":    "3600",
		})
	}))
}

func baseConfig(stsURL, iamURL, idtURL string) Config {
	return Config{
		AzureToken:          "azure-test-token",
		ProjectNumber:       "123456789",
		PoolID:              "test-pool",
		ProviderID:          "test-provider",
		ServiceAccountEmail: "sa@project.iam.gserviceaccount.com",
		ProjectID:           "test-project",
		APIKey:              "test-api-key",
		UID:                 "test-uid",
		OverrideSTSURL:      stsURL,
		OverrideIAMURL:      iamURL,
		OverrideIDTURL:      idtURL,
	}
}

func TestExchangeForFirebaseIDToken_Success(t *testing.T) {
	sts := newMockSTS(t, "federated-token", http.StatusOK)
	iam := newMockIAM(t, "sa-access-token", "signed-custom-jwt")
	idt := newMockIDT(t, "firebase-id-token")
	defer sts.Close()
	defer iam.Close()
	defer idt.Close()

	idToken, err := ExchangeForFirebaseIDToken(context.Background(),
		baseConfig(sts.URL, iam.URL, idt.URL))

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if idToken != "firebase-id-token" {
		t.Errorf("got %q, want %q", idToken, "firebase-id-token")
	}
}

func TestExchangeForFirebaseIDToken_STSError(t *testing.T) {
	sts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error":             "invalid_grant",
			"error_description": "bad subject token",
		})
	}))
	defer sts.Close()

	_, err := ExchangeForFirebaseIDToken(context.Background(),
		baseConfig(sts.URL, "http://unused", "http://unused"))

	if err == nil {
		t.Fatal("expected error from STS, got nil")
	}
	if !strings.Contains(err.Error(), "invalid_grant") {
		t.Errorf("error %q does not mention invalid_grant", err.Error())
	}
}

func TestExchangeForFirebaseIDToken_SAError(t *testing.T) {
	sts := newMockSTS(t, "federated-token", http.StatusOK)
	iam := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": map[string]interface{}{
				"message": "permission denied on SA",
				"code":    403,
			},
		})
	}))
	defer sts.Close()
	defer iam.Close()

	_, err := ExchangeForFirebaseIDToken(context.Background(),
		baseConfig(sts.URL, iam.URL, "http://unused"))

	if err == nil {
		t.Fatal("expected error from SA impersonation, got nil")
	}
	if !strings.Contains(err.Error(), "permission denied") {
		t.Errorf("error %q does not mention permission denied", err.Error())
	}
}

func TestExchangeForFirebaseIDToken_FirebaseError(t *testing.T) {
	sts := newMockSTS(t, "federated-token", http.StatusOK)
	iam := newMockIAM(t, "sa-access-token", "signed-custom-jwt")
	idt := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": map[string]interface{}{
				"message": "INVALID_CUSTOM_TOKEN",
				"code":    400,
			},
		})
	}))
	defer sts.Close()
	defer iam.Close()
	defer idt.Close()

	_, err := ExchangeForFirebaseIDToken(context.Background(),
		baseConfig(sts.URL, iam.URL, idt.URL))

	if err == nil {
		t.Fatal("expected Firebase error, got nil")
	}
	if !strings.Contains(err.Error(), "INVALID_CUSTOM_TOKEN") {
		t.Errorf("error %q does not mention INVALID_CUSTOM_TOKEN", err.Error())
	}
}
