// Package middleware provides JWT validation for Firebase ID tokens issued
// by Google's securetoken service (real Firebase, not the emulator).
//
// Configuration (env vars):
//   GCP_PROJECT_ID        — Firebase project ID; determines issuer and audience.
//   JWKS_URL              — optional override for the Firebase JWKS endpoint.
//   BACKEND_REQUIRED_ROLE — optional role check, format "field:value"
//                           e.g. "htpa_roles:HTPA_USER"
//                           The named claim must be a string equal to value,
//                           or an array containing value. Returns 403 if absent.
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

// requiredRole holds the optional role claim check parsed from BACKEND_REQUIRED_ROLE.
var requiredRole struct {
	field string // claim field name, e.g. "htpa_roles"
	value string // required value, e.g. "HTPA_USER"
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

	// Optional role enforcement: BACKEND_REQUIRED_ROLE=field:value
	if v := os.Getenv("BACKEND_REQUIRED_ROLE"); v != "" {
		parts := strings.SplitN(v, ":", 2)
		if len(parts) == 2 {
			requiredRole.field = parts[0]
			requiredRole.value = parts[1]
			log.Printf("role check enabled: claim %q must contain %q", requiredRole.field, requiredRole.value)
		} else {
			log.Printf("WARNING: BACKEND_REQUIRED_ROLE %q is not in field:value format — ignored", v)
		}
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

func validateFirebase(tokenStr string) (jwt.MapClaims, error) {
	token, err := jwt.ParseWithClaims(tokenStr, jwt.MapClaims{}, func(t *jwt.Token) (interface{}, error) {
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
		return nil, fmt.Errorf("firebase token invalid: %w", err)
	}
	if !token.Valid {
		return nil, fmt.Errorf("firebase token not valid")
	}
	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		return nil, fmt.Errorf("unexpected claims type")
	}
	return claims, nil
}

// hasRole returns true if claims[field] is a string equal to value,
// or an array that contains value.
func hasRole(claims jwt.MapClaims, field, value string) bool {
	v, ok := claims[field]
	if !ok {
		return false
	}
	switch t := v.(type) {
	case string:
		return t == value
	case []interface{}:
		for _, item := range t {
			if s, ok := item.(string); ok && s == value {
				return true
			}
		}
	}
	return false
}

func unauthorized(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	w.Write([]byte(`{"error":"unauthorized"}`))
}

func forbidden(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusForbidden)
	w.Write([]byte(`{"error":"forbidden","reason":"missing required role"}`))
}

// ValidateToken middleware validates Firebase ID tokens issued by the real
// Firebase Auth service (securetoken.google.com).
// If BACKEND_REQUIRED_ROLE is set, it also enforces that the token carries the
// required claim value, returning 403 when the claim is absent.
func ValidateToken(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeader := r.Header.Get("Authorization")
		if !strings.HasPrefix(authHeader, "Bearer ") {
			unauthorized(w)
			return
		}
		tokenStr := strings.TrimPrefix(authHeader, "Bearer ")

		claims, err := validateFirebase(tokenStr)
		if err != nil {
			log.Printf("auth: %v", err)
			unauthorized(w)
			return
		}

		if requiredRole.field != "" && !hasRole(claims, requiredRole.field, requiredRole.value) {
			log.Printf("auth: token missing required role %s:%s", requiredRole.field, requiredRole.value)
			forbidden(w)
			return
		}

		next.ServeHTTP(w, r.WithContext(
			context.WithValue(r.Context(), contextKeyProject{}, firebaseConfig.audience),
		))
	})
}

type contextKeyProject struct{}
