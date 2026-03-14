// Package middleware provides JWT validation for Firebase ID tokens issued
// by Google's securetoken service (real Firebase, not the emulator).
//
// Configuration (env vars):
//   GCP_PROJECT_ID  — Firebase project ID; determines issuer and audience.
//   JWKS_URL        — optional override for the Firebase JWKS endpoint.
package middleware

import (
	"context"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/big"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Firebase's public JWKS endpoint — keys rotate roughly every 6 hours.
const firebaseJWKSURL = "https://www.googleapis.com/service_accounts/v1/jwk/securetoken@system.gserviceaccount.com"

// firebaseConfig is resolved once at startup from env vars.
var firebaseConfig struct {
	jwksURL  string
	issuer   string
	audience string // == GCP project ID
}

func init() {
	projectID := os.Getenv("GCP_PROJECT_ID")
	if projectID == "" {
		log.Println("WARNING: GCP_PROJECT_ID is not set; token validation will fail")
	}
	firebaseConfig.jwksURL  = firebaseJWKSURL
	firebaseConfig.issuer   = "https://securetoken.google.com/" + projectID
	firebaseConfig.audience = projectID

	// Allow explicit override (useful for testing).
	if v := os.Getenv("JWKS_URL"); v != "" {
		firebaseConfig.jwksURL = v
	}
}

// jwksCache caches RSA public keys fetched from the Firebase JWKS endpoint.
// Keys are re-fetched whenever an unknown kid is encountered.
type jwksCache struct {
	mu      sync.RWMutex
	keys    map[string]*rsa.PublicKey
	fetched time.Time
}

var cache = &jwksCache{keys: map[string]*rsa.PublicKey{}}

type jwksResponse struct {
	Keys []struct {
		Kid string `json:"kid"`
		N   string `json:"n"`
		E   string `json:"e"`
		Kty string `json:"kty"`
	} `json:"keys"`
}

func (c *jwksCache) get(kid string) (*rsa.PublicKey, error) {
	c.mu.RLock()
	key, ok := c.keys[kid]
	c.mu.RUnlock()
	if ok {
		return key, nil
	}
	return c.refresh(kid)
}

func (c *jwksCache) refresh(kid string) (*rsa.PublicKey, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	resp, err := http.Get(firebaseConfig.jwksURL)
	if err != nil {
		return nil, fmt.Errorf("fetching Firebase JWKS: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	var jwks jwksResponse
	if err := json.Unmarshal(body, &jwks); err != nil {
		return nil, fmt.Errorf("parsing Firebase JWKS: %w", err)
	}

	for _, k := range jwks.Keys {
		if k.Kty != "RSA" {
			continue
		}
		nBytes, err := base64.RawURLEncoding.DecodeString(k.N)
		if err != nil {
			continue
		}
		eBytes, err := base64.RawURLEncoding.DecodeString(k.E)
		if err != nil {
			continue
		}
		e := int(new(big.Int).SetBytes(eBytes).Int64())
		c.keys[k.Kid] = &rsa.PublicKey{
			N: new(big.Int).SetBytes(nBytes),
			E: e,
		}
	}
	c.fetched = time.Now()

	if key, ok := c.keys[kid]; ok {
		return key, nil
	}
	return nil, fmt.Errorf("key id %q not found in Firebase JWKS", kid)
}

func validateFirebase(tokenStr string) error {
	token, err := jwt.Parse(tokenStr, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodRSA); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		kid, _ := t.Header["kid"].(string)
		return cache.get(kid)
	},
		jwt.WithIssuer(firebaseConfig.issuer),
		jwt.WithAudience(firebaseConfig.audience),
		jwt.WithExpirationRequired(),
	)
	if err != nil {
		return fmt.Errorf("firebase token invalid: %w", err)
	}
	if !token.Valid {
		return fmt.Errorf("firebase token not valid")
	}
	return nil
}

func unauthorized(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	w.Write([]byte(`{"error":"unauthorized"}`))
}

// ValidateToken middleware validates Firebase ID tokens issued by the real
// Firebase Auth service (securetoken.google.com).
func ValidateToken(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeader := r.Header.Get("Authorization")
		if !strings.HasPrefix(authHeader, "Bearer ") {
			unauthorized(w)
			return
		}
		tokenStr := strings.TrimPrefix(authHeader, "Bearer ")

		if err := validateFirebase(tokenStr); err != nil {
			log.Printf("auth: %v", err)
			unauthorized(w)
			return
		}

		next.ServeHTTP(w, r.WithContext(
			context.WithValue(r.Context(), contextKeyProject{}, firebaseConfig.audience),
		))
	})
}

type contextKeyProject struct{}
