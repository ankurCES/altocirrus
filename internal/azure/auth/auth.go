// Package azureauth implements fake Azure AD / Entra ID token endpoints
// for the AltoCirrus cloud emulator.
package azureauth

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"fmt"
	"math/big"
	"net/http"
	"sync"
	"time"

	"github.com/altocirrus/altocirrus/internal/config"
	"github.com/altocirrus/altocirrus/internal/server"
	"github.com/golang-jwt/jwt/v5"
)

var (
	signingKey  *rsa.PrivateKey
	keyID       string
	initKeyOnce sync.Once
)

// initKey generates the RSA 2048 signing key pair exactly once.
func initKey() {
	initKeyOnce.Do(func() {
		var err error
		signingKey, err = rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			panic("azureauth: failed to generate RSA key: " + err.Error())
		}
		keyID = newUUID()
	})
}

// RegisterRoutes registers Azure AD / Entra ID auth endpoints on the mux.
func RegisterRoutes(mux *http.ServeMux, cfg *config.Config) {
	initKey()

	mux.HandleFunc("POST /{tenantId}/oauth2/v2.0/token", handleToken(cfg))
	mux.HandleFunc("GET /.well-known/openid-configuration", handleOpenIDConfig(cfg))
	mux.HandleFunc("GET /common/discovery/v2.0/keys", handleJWKS())
}

// tokenResponse is the OAuth 2.0 token response body.
type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	ExtExpiresIn int    `json:"ext_expires_in"`
}

// handleToken returns a handler for POST /{tenantId}/oauth2/v2.0/token.
func handleToken(cfg *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tenantID := r.PathValue("tenantId")
		if tenantID == "" {
			server.AzureError(w, "InvalidRequest", "tenantId is required", http.StatusBadRequest)
			return
		}

		if err := r.ParseForm(); err != nil {
			server.AzureError(w, "InvalidRequest", "malformed form body", http.StatusBadRequest)
			return
		}

		grantType := r.FormValue("grant_type")
		clientID := r.FormValue("client_id")
		scope := r.FormValue("scope")

		if grantType == "" {
			server.AzureError(w, "InvalidRequest", "grant_type is required", http.StatusBadRequest)
			return
		}

		// Determine the subject: use client_id if provided, otherwise generate one.
		sub := clientID
		if sub == "" {
			sub = newUUID()
		}

		now := time.Now()
		claims := jwt.MapClaims{
			"iss": fmt.Sprintf("http://localhost:%d/%s/v2.0", cfg.Port, tenantID),
			"sub": sub,
			"aud": scope,
			"exp": now.Add(1 * time.Hour).Unix(),
			"iat": now.Unix(),
			"nbf": now.Unix(),
			"tid": tenantID,
			"oid": newUUID(),
			"scp": scope,
		}

		token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
		token.Header["kid"] = keyID

		signed, err := token.SignedString(signingKey)
		if err != nil {
			server.AzureError(w, "InternalError", "failed to sign token", http.StatusInternalServerError)
			return
		}

		server.WriteJSON(w, http.StatusOK, tokenResponse{
			AccessToken:  signed,
			TokenType:    "Bearer",
			ExpiresIn:    3600,
			ExtExpiresIn: 3600,
		})
	}
}

// openIDConfiguration is the OIDC discovery document.
type openIDConfiguration struct {
	Issuer                           string   `json:"issuer"`
	AuthorizationEndpoint            string   `json:"authorization_endpoint"`
	TokenEndpoint                    string   `json:"token_endpoint"`
	TokenEndpointAuthMethodsSupported []string `json:"token_endpoint_auth_methods_supported"`
	JwksURI                          string   `json:"jwks_uri"`
	ResponseTypesSupported           []string `json:"response_types_supported"`
	SubjectTypesSupported            []string `json:"subject_types_supported"`
	IDTokenSigningAlgValuesSupported []string `json:"id_token_signing_alg_values_supported"`
	ScopesSupported                  []string `json:"scopes_supported"`
	ClaimsSupported                  []string `json:"claims_supported"`
}

// handleOpenIDConfig returns a handler for GET /.well-known/openid-configuration.
func handleOpenIDConfig(cfg *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		base := fmt.Sprintf("http://localhost:%d", cfg.Port)
		tenantID := cfg.Azure.TenantID

		doc := openIDConfiguration{
			Issuer:                           fmt.Sprintf("%s/%s/v2.0", base, tenantID),
			AuthorizationEndpoint:            fmt.Sprintf("%s/%s/oauth2/v2.0/authorize", base, tenantID),
			TokenEndpoint:                    fmt.Sprintf("%s/%s/oauth2/v2.0/token", base, tenantID),
			TokenEndpointAuthMethodsSupported: []string{"client_secret_post", "client_secret_basic"},
			JwksURI:                          fmt.Sprintf("%s/common/discovery/v2.0/keys", base),
			ResponseTypesSupported:           []string{"code", "id_token", "code id_token", "token"},
			SubjectTypesSupported:            []string{"pairwise"},
			IDTokenSigningAlgValuesSupported: []string{"RS256"},
			ScopesSupported:                  []string{"openid", "profile", "email", "offline_access"},
			ClaimsSupported:                  []string{"sub", "iss", "aud", "exp", "iat", "nbf", "name", "email", "oid", "tid"},
		}

		server.WriteJSON(w, http.StatusOK, doc)
	}
}

// jwksResponse is the JSON Web Key Set response.
type jwksResponse struct {
	Keys []jwk `json:"keys"`
}

// jwk represents a single JSON Web Key.
type jwk struct {
	Kty string `json:"kty"`
	Use string `json:"use"`
	Kid string `json:"kid"`
	Alg string `json:"alg"`
	N   string `json:"n"`
	E   string `json:"e"`
}

// handleJWKS returns a handler for GET /common/discovery/v2.0/keys.
func handleJWKS() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		pub := &signingKey.PublicKey

		// Encode modulus (n) as base64url, unpadded.
		nBytes := pub.N.Bytes()
		nEncoded := base64.RawURLEncoding.EncodeToString(nBytes)

		// Encode exponent (e) as base64url, unpadded.
		eBytes := big.NewInt(int64(pub.E)).Bytes()
		eEncoded := base64.RawURLEncoding.EncodeToString(eBytes)

		resp := jwksResponse{
			Keys: []jwk{
				{
					Kty: "RSA",
					Use: "sig",
					Kid: keyID,
					Alg: "RS256",
					N:   nEncoded,
					E:   eEncoded,
				},
			},
		}

		server.WriteJSON(w, http.StatusOK, resp)
	}
}

// newUUID generates a random UUID v4 string using crypto/rand.
func newUUID() string {
	var uuid [16]byte
	_, err := rand.Read(uuid[:])
	if err != nil {
		return "00000000-0000-0000-0000-000000000000"
	}
	// Set version (4) and variant (RFC 4122) bits.
	uuid[6] = (uuid[6] & 0x0f) | 0x40
	uuid[8] = (uuid[8] & 0x3f) | 0x80

	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		uuid[0:4], uuid[4:6], uuid[6:8], uuid[8:10], uuid[10:16])
}
