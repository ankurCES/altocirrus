package azureauth_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	azureauth "github.com/altocirrus/altocirrus/internal/azure/auth"
	"github.com/altocirrus/altocirrus/internal/config"
	"github.com/golang-jwt/jwt/v5"
)

// newTestServer creates an httptest.Server with all Azure auth routes registered.
func newTestServer(t *testing.T) (*httptest.Server, *config.Config) {
	t.Helper()
	cfg := &config.Config{
		Port: 4567,
		Azure: config.AzureConfig{
			SubscriptionID: "test-sub-id",
			TenantID:       "test-tenant-id",
			Region:         "eastus",
		},
	}
	mux := http.NewServeMux()
	azureauth.RegisterRoutes(mux, cfg)
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts, cfg
}

func TestTokenEndpoint(t *testing.T) {
	ts, _ := newTestServer(t)

	tenantID := "my-tenant-123"
	form := url.Values{}
	form.Set("grant_type", "client_credentials")
	form.Set("client_id", "my-client-app")
	form.Set("scope", "https://management.azure.com/.default")

	resp, err := http.Post(
		ts.URL+"/"+tenantID+"/oauth2/v2.0/token",
		"application/x-www-form-urlencoded",
		strings.NewReader(form.Encode()),
	)
	if err != nil {
		t.Fatalf("POST token: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var body struct {
		AccessToken  string `json:"access_token"`
		TokenType    string `json:"token_type"`
		ExpiresIn    int    `json:"expires_in"`
		ExtExpiresIn int    `json:"ext_expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if body.AccessToken == "" {
		t.Fatal("access_token is empty")
	}
	if body.TokenType != "Bearer" {
		t.Errorf("token_type = %q, want Bearer", body.TokenType)
	}
	if body.ExpiresIn != 3600 {
		t.Errorf("expires_in = %d, want 3600", body.ExpiresIn)
	}

	// Parse the JWT without verification (we don't have the public key here)
	// to inspect claims.
	parser := jwt.NewParser(jwt.WithoutClaimsValidation())
	token, _, err := parser.ParseUnverified(body.AccessToken, jwt.MapClaims{})
	if err != nil {
		t.Fatalf("parse JWT: %v", err)
	}

	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		t.Fatal("claims are not MapClaims")
	}

	// Check issuer contains the tenant ID.
	iss, _ := claims["iss"].(string)
	if !strings.Contains(iss, tenantID) {
		t.Errorf("iss = %q, want it to contain tenant %q", iss, tenantID)
	}
	if !strings.HasSuffix(iss, "/v2.0") {
		t.Errorf("iss = %q, want it to end with /v2.0", iss)
	}

	// Check audience matches the requested scope.
	aud, _ := claims["aud"].(string)
	if aud != "https://management.azure.com/.default" {
		t.Errorf("aud = %q, want %q", aud, "https://management.azure.com/.default")
	}

	// Check tenant ID claim.
	tid, _ := claims["tid"].(string)
	if tid != tenantID {
		t.Errorf("tid = %q, want %q", tid, tenantID)
	}

	// Check exp is a future timestamp.
	exp, _ := claims["exp"].(float64)
	if exp == 0 {
		t.Error("exp claim is missing or zero")
	}
}

func TestTokenEndpointMissingGrantType(t *testing.T) {
	ts, _ := newTestServer(t)

	form := url.Values{}
	form.Set("client_id", "my-client-app")

	resp, err := http.Post(
		ts.URL+"/some-tenant/oauth2/v2.0/token",
		"application/x-www-form-urlencoded",
		strings.NewReader(form.Encode()),
	)
	if err != nil {
		t.Fatalf("POST token: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}

	var errResp struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&errResp); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if errResp.Error.Code != "InvalidRequest" {
		t.Errorf("error code = %q, want InvalidRequest", errResp.Error.Code)
	}
}

func TestOIDCDiscovery(t *testing.T) {
	ts, cfg := newTestServer(t)

	resp, err := http.Get(ts.URL + "/.well-known/openid-configuration")
	if err != nil {
		t.Fatalf("GET openid-configuration: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var doc struct {
		Issuer        string `json:"issuer"`
		TokenEndpoint string `json:"token_endpoint"`
		JwksURI       string `json:"jwks_uri"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if doc.Issuer == "" {
		t.Error("issuer is empty")
	}
	// Issuer should contain the configured tenant ID.
	if !strings.Contains(doc.Issuer, cfg.Azure.TenantID) {
		t.Errorf("issuer = %q, want it to contain tenant %q", doc.Issuer, cfg.Azure.TenantID)
	}

	if doc.TokenEndpoint == "" {
		t.Error("token_endpoint is empty")
	}
	if !strings.Contains(doc.TokenEndpoint, "/oauth2/v2.0/token") {
		t.Errorf("token_endpoint = %q, want it to contain /oauth2/v2.0/token", doc.TokenEndpoint)
	}

	if doc.JwksURI == "" {
		t.Error("jwks_uri is empty")
	}
	if !strings.Contains(doc.JwksURI, "/common/discovery/v2.0/keys") {
		t.Errorf("jwks_uri = %q, want it to contain /common/discovery/v2.0/keys", doc.JwksURI)
	}
}

func TestJWKS(t *testing.T) {
	ts, _ := newTestServer(t)

	resp, err := http.Get(ts.URL + "/common/discovery/v2.0/keys")
	if err != nil {
		t.Fatalf("GET JWKS: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var jwks struct {
		Keys []struct {
			Kty string `json:"kty"`
			Use string `json:"use"`
			Kid string `json:"kid"`
			Alg string `json:"alg"`
			N   string `json:"n"`
			E   string `json:"e"`
		} `json:"keys"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&jwks); err != nil {
		t.Fatalf("decode JWKS: %v", err)
	}

	if len(jwks.Keys) == 0 {
		t.Fatal("keys array is empty")
	}

	key := jwks.Keys[0]
	if key.Kty != "RSA" {
		t.Errorf("kty = %q, want RSA", key.Kty)
	}
	if key.Alg != "RS256" {
		t.Errorf("alg = %q, want RS256", key.Alg)
	}
	if key.Kid == "" {
		t.Error("kid is empty")
	}
	if key.N == "" {
		t.Error("n (modulus) is empty")
	}
	if key.E == "" {
		t.Error("e (exponent) is empty")
	}
}

func TestJWKSKeyMatchesTokenSigner(t *testing.T) {
	ts, _ := newTestServer(t)

	// First fetch the JWKS to get the kid.
	jwksResp, err := http.Get(ts.URL + "/common/discovery/v2.0/keys")
	if err != nil {
		t.Fatalf("GET JWKS: %v", err)
	}
	defer jwksResp.Body.Close()

	var jwks struct {
		Keys []struct {
			Kid string `json:"kid"`
		} `json:"keys"`
	}
	if err := json.NewDecoder(jwksResp.Body).Decode(&jwks); err != nil {
		t.Fatalf("decode JWKS: %v", err)
	}
	if len(jwks.Keys) == 0 {
		t.Fatal("no JWKS keys")
	}
	expectedKid := jwks.Keys[0].Kid

	// Now request a token and verify its kid header matches.
	form := url.Values{}
	form.Set("grant_type", "client_credentials")
	form.Set("client_id", "test-app")
	form.Set("scope", "test-scope")

	tokenResp, err := http.Post(
		ts.URL+"/test-tenant/oauth2/v2.0/token",
		"application/x-www-form-urlencoded",
		strings.NewReader(form.Encode()),
	)
	if err != nil {
		t.Fatalf("POST token: %v", err)
	}
	defer tokenResp.Body.Close()

	var tokenBody struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(tokenResp.Body).Decode(&tokenBody); err != nil {
		t.Fatalf("decode token: %v", err)
	}

	parser := jwt.NewParser(jwt.WithoutClaimsValidation())
	token, _, err := parser.ParseUnverified(tokenBody.AccessToken, jwt.MapClaims{})
	if err != nil {
		t.Fatalf("parse JWT: %v", err)
	}

	tokenKid, ok := token.Header["kid"].(string)
	if !ok {
		t.Fatal("token header missing kid")
	}
	if tokenKid != expectedKid {
		t.Errorf("token kid = %q, JWKS kid = %q; want them to match", tokenKid, expectedKid)
	}
}
