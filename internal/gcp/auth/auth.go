package gcpauth

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/altocirrus/altocirrus/internal/config"
)

// RegisterRoutes adds GCP OAuth2 fake-auth and metadata endpoints to mux.
func RegisterRoutes(mux *http.ServeMux, cfg *config.Config) {
	mux.HandleFunc("POST /token", tokenHandler)
	mux.HandleFunc("POST /oauth2/v4/token", tokenHandler)
	mux.HandleFunc("GET /computeMetadata/v1/project/project-id", metadataProjectID(cfg))
	mux.HandleFunc("GET /computeMetadata/v1/project/numeric-project-id", metadataNumericProjectID(cfg))
}

// tokenResponse is the JSON body returned by the OAuth2 token endpoint.
type tokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int    `json:"expires_in"`
}

func tokenHandler(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	// grant_type, client_id, client_secret are accepted but not validated —
	// this is a fake auth server for local development.

	suffix, err := randomHex(16)
	if err != nil {
		http.Error(w, "failed to generate token", http.StatusInternalServerError)
		return
	}

	resp := tokenResponse{
		AccessToken: fmt.Sprintf("ya29.altocirrus-fake-token-%s", suffix),
		TokenType:   "Bearer",
		ExpiresIn:   3600,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func metadataProjectID(cfg *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireMetadataFlavor(w, r) {
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprint(w, cfg.GCP.ProjectID)
	}
}

func metadataNumericProjectID(cfg *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireMetadataFlavor(w, r) {
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprint(w, cfg.GCP.ProjectNumber)
	}
}

// requireMetadataFlavor checks that the Metadata-Flavor: Google header is
// present. Returns false (and writes a 403) if missing.
func requireMetadataFlavor(w http.ResponseWriter, r *http.Request) bool {
	if r.Header.Get("Metadata-Flavor") != "Google" {
		http.Error(w, "missing Metadata-Flavor: Google header", http.StatusForbidden)
		return false
	}
	return true
}

// randomHex returns n random bytes encoded as a hex string.
func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
