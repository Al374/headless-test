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

const (
	keycloakJWKSURL = "http://localhost:8180/realms/test/protocol/openid-connect/certs"
	keycloakIssuer  = "http://localhost:8180/realms/test"
	backendAudience = "backend-api"
)

// jwksCache caches public keys fetched from a JWKS endpoint.
type jwksCache struct {
	mu      sync.RWMutex
	keys    map[string]*rsa.PublicKey
	fetched time.Time
}

var keycloakCache = &jwksCache{keys: map[string]*rsa.PublicKey{}}

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

	resp, err := http.Get(keycloakJWKSURL)
	if err != nil {
		return nil, fmt.Errorf("fetching JWKS: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	var jwks jwksResponse
	if err := json.Unmarshal(body, &jwks); err != nil {
		return nil, fmt.Errorf("parsing JWKS: %w", err)
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
		pub := &rsa.PublicKey{
			N: new(big.Int).SetBytes(nBytes),
			E: e,
		}
		c.keys[k.Kid] = pub
	}
	c.fetched = time.Now()

	if key, ok := c.keys[kid]; ok {
		return key, nil
	}
	return nil, fmt.Errorf("key id %q not found in JWKS", kid)
}

func validateKeycloak(tokenStr string) error {
	token, err := jwt.Parse(tokenStr, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodRSA); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		kid, _ := t.Header["kid"].(string)
		return keycloakCache.get(kid)
	}, jwt.WithIssuer(keycloakIssuer), jwt.WithExpirationRequired())
	if err != nil {
		return fmt.Errorf("keycloak token invalid: %w", err)
	}

	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		return fmt.Errorf("claims not map")
	}

	// Check audience contains backend-api.
	audOK := false
	switch v := claims["aud"].(type) {
	case string:
		audOK = v == backendAudience
	case []interface{}:
		for _, a := range v {
			if s, ok := a.(string); ok && s == backendAudience {
				audOK = true
				break
			}
		}
	}
	if !audOK {
		return fmt.Errorf("audience %v does not contain %q", claims["aud"], backendAudience)
	}
	return nil
}

// validateFirebaseEmulator validates a Firebase ID token issued by the local emulator.
// In emulator mode the token is a standard JWT signed with a key the emulator controls.
// We trust the token if FIREBASE_AUTH_EMULATOR_HOST is set and the issuer matches the
// expected emulator issuer pattern, skipping signature verification (emulator tokens use
// ephemeral keys that change across restarts).
func validateFirebaseEmulator(tokenStr string) error {
	// Parse without verification to read claims first.
	p := jwt.NewParser()
	token, _, err := p.ParseUnverified(tokenStr, jwt.MapClaims{})
	if err != nil {
		return fmt.Errorf("parsing firebase token: %w", err)
	}
	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		return fmt.Errorf("claims not map")
	}

	// Verify expiry manually (jwt/v5 removed MapClaims.Valid).
	if exp, ok := claims["exp"].(float64); ok {
		if time.Now().Unix() > int64(exp) {
			return fmt.Errorf("firebase token expired")
		}
	}

	iss, _ := claims["iss"].(string)
	if !strings.Contains(iss, "firebase") && !strings.Contains(iss, "securetoken") {
		return fmt.Errorf("unexpected firebase issuer: %s", iss)
	}

	return nil
}

func unauthorized(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	w.Write([]byte(`{"error":"unauthorized"}`))
}

// ValidateToken middleware authenticates requests using Keycloak (Option A) or
// Firebase emulator (Option B) depending on the token's issuer claim.
func ValidateToken(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeader := r.Header.Get("Authorization")
		if !strings.HasPrefix(authHeader, "Bearer ") {
			unauthorized(w)
			return
		}
		tokenStr := strings.TrimPrefix(authHeader, "Bearer ")

		// Peek at the issuer without full verification to decide which path to use.
		p := jwt.NewParser()
		unverified, _, err := p.ParseUnverified(tokenStr, jwt.MapClaims{})
		if err != nil {
			log.Printf("auth: failed to parse token header: %v", err)
			unauthorized(w)
			return
		}
		claims, _ := unverified.Claims.(jwt.MapClaims)
		iss, _ := claims["iss"].(string)

		emulatorHost := os.Getenv("FIREBASE_AUTH_EMULATOR_HOST")

		var validationErr error
		if emulatorHost != "" && (strings.Contains(iss, "firebase") || strings.Contains(iss, "securetoken")) {
			validationErr = validateFirebaseEmulator(tokenStr)
		} else {
			validationErr = validateKeycloak(tokenStr)
		}

		if validationErr != nil {
			log.Printf("auth: %v", validationErr)
			unauthorized(w)
			return
		}

		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), contextKeyIss{}, iss)))
	})
}

type contextKeyIss struct{}
