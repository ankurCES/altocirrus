package gcpauth_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/altocirrus/altocirrus/internal/config"
	gcpauth "github.com/altocirrus/altocirrus/internal/gcp/auth"
)

// newTestServer creates an httptest.Server with the GCP auth routes registered.
func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	cfg := &config.Config{
		GCP: config.GCPConfig{
			ProjectID:     "test-project-42",
			ProjectNumber: "999888777",
			Region:        "us-central1",
		},
	}
	mux := http.NewServeMux()
	gcpauth.RegisterRoutes(mux, cfg)
	return httptest.NewServer(mux)
}

func TestTokenEndpoint(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()

	// Test both token paths.
	paths := []string{"/token", "/oauth2/v4/token"}

	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			resp, err := http.Post(ts.URL+path, "application/x-www-form-urlencoded",
				strings.NewReader("grant_type=client_credentials"))
			if err != nil {
				t.Fatalf("POST %s: %v", path, err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusOK {
				t.Fatalf("expected 200, got %d", resp.StatusCode)
			}

			ct := resp.Header.Get("Content-Type")
			if !strings.HasPrefix(ct, "application/json") {
				t.Fatalf("expected Content-Type application/json, got %q", ct)
			}

			var body struct {
				AccessToken string `json:"access_token"`
				TokenType   string `json:"token_type"`
				ExpiresIn   int    `json:"expires_in"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
				t.Fatalf("failed to decode response: %v", err)
			}

			if body.AccessToken == "" {
				t.Fatal("access_token is empty")
			}
			if !strings.HasPrefix(body.AccessToken, "ya29.altocirrus-fake-token-") {
				t.Errorf("access_token does not have expected prefix: %q", body.AccessToken)
			}
			if body.TokenType != "Bearer" {
				t.Errorf("expected token_type=Bearer, got %q", body.TokenType)
			}
			if body.ExpiresIn != 3600 {
				t.Errorf("expected expires_in=3600, got %d", body.ExpiresIn)
			}
		})
	}
}

func TestTokenEndpointUniqueness(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()

	// Two consecutive calls should produce different tokens.
	getToken := func() string {
		resp, err := http.Post(ts.URL+"/token", "application/x-www-form-urlencoded", nil)
		if err != nil {
			t.Fatalf("POST /token: %v", err)
		}
		defer resp.Body.Close()
		var body struct {
			AccessToken string `json:"access_token"`
		}
		json.NewDecoder(resp.Body).Decode(&body)
		return body.AccessToken
	}

	t1 := getToken()
	t2 := getToken()
	if t1 == t2 {
		t.Errorf("expected unique tokens, both were %q", t1)
	}
}

func TestMetadataProjectID(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()

	t.Run("with Metadata-Flavor header", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodGet, ts.URL+"/computeMetadata/v1/project/project-id", nil)
		req.Header.Set("Metadata-Flavor", "Google")

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("GET metadata: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200, got %d", resp.StatusCode)
		}

		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		if got := string(body); got != "test-project-42" {
			t.Errorf("expected project ID %q, got %q", "test-project-42", got)
		}
	})

	t.Run("without Metadata-Flavor header returns 403", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodGet, ts.URL+"/computeMetadata/v1/project/project-id", nil)
		// No Metadata-Flavor header.

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("GET metadata: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("expected 403, got %d", resp.StatusCode)
		}
	})
}

func TestMetadataNumericProjectID(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/computeMetadata/v1/project/numeric-project-id", nil)
	req.Header.Set("Metadata-Flavor", "Google")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET numeric project id: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	if got := string(body); got != "999888777" {
		t.Errorf("expected numeric project id %q, got %q", "999888777", got)
	}
}
