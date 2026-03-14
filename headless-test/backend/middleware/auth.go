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

const defaultJWKSURL   = "http://localhost:8180/realms/test/protocol/openid-connect/certs"
const defaultIssuer    = "http://localhost:8180/realms/test"
const defaultAudience  = "backend-api"

// jwksConfig holds runtime-resolved values read once from env at startup.
var jwksConfig struct {
	jwksURL  string
	issuer   string
	audience string
}

func init() {
	jwksConfig.jwksURL = defaultJWKSURL
	jwksConfig.issuer  = defaultIssuer
	jwksConfig.audience = defaultAudience

	// AZURE_TENANT_ID set to a real tenant UUID → use Azure AD endpoints.
	// When it is "test" (Keycloak local mode) the defaults above are used.
	// Azure issues v1.0 tokens by default (accessTokenAcceptedVersion=null in manifest).
	// v1 tokens carry issuer "https://sts.windows.net/{tenant}/" and use the /keys JWKS URL.
	// v2 tokens carry issuer "https://login.microsoftonline.com/{tenant}/v2.0" and use /v2.0/keys.
	// The /v2.0/keys endpoint serves both v1 and v2 signing keys, so we use it for both.
	if tid := os.Getenv("AZURE_TENANT_ID"); tid != "" && tid != "test" {
		jwksConfig.jwksURL = "https://login.microsoftonline.com/" + tid + "/discovery/v2.0/keys"
		// Default to v1 issuer; override with JWT_ISSUER if the app uses v2 tokens.
		jwksConfig.issuer = "https://sts.windows.net/" + tid + "/"
	}
	// Allow explicit overrides regardless of AZURE_TENANT_ID.
	if v := os.Getenv("JWKS_URL");    v != "" { jwksConfig.jwksURL  = v }
	if v := os.Getenv("JWT_ISSUER");  v != "" { jwksConfig.issuer   = v }
	if v := os.Getenv("BACKEND_CLIENT_ID"); v != "" { jwksConfig.audience = v }
}

// jwksCache caches public keys fetched from a JWKS endpoint.
type jwksCache struct {
	mu      sync.RWMutex
	keys    map[string]*rsa.PublicKey
	fetched time.Time
}

var rsaCache = &jwksCache{keys: map[string]*rsa.PublicKey{}}

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

	resp, err := http.Get(jwksConfig.jwksURL)
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

// validateRS256JWT validates a token using the configured JWKS endpoint and issuer.
// Works for both Keycloak and Azure AD — both use RS256 + JWKS.
func validateRS256JWT(tokenStr string) error {
	token, err := jwt.Parse(tokenStr, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodRSA); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		kid, _ := t.Header["kid"].(string)
		return rsaCache.get(kid)
	}, jwt.WithIssuer(jwksConfig.issuer), jwt.WithExpirationRequired())
	if err != nil {
		return fmt.Errorf("token invalid: %w", err)
	}

	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		return fmt.Errorf("claims not map")
	}

	// Check audience contains the expected backend client ID.
	audOK := false
	switch v := claims["aud"].(type) {
	case string:
		audOK = v == jwksConfig.audience
	case []interface{}:
		for _, a := range v {
			if s, ok := a.(string); ok && s == jwksConfig.audience {
				audOK = true
				break
			}
		}
	}
	if !audOK {
		return fmt.Errorf("audience %v does not contain %q", claims["aud"], jwksConfig.audience)
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
			validationErr = validateRS256JWT(tokenStr)
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
