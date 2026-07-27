package keyvault

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/altocirrus/altocirrus/internal/config"
	"github.com/altocirrus/altocirrus/internal/server"
	"github.com/altocirrus/altocirrus/internal/storage"
)

const namespace = "azure:keyvault:secrets"

// ---------------------------------------------------------------------------
// Models
// ---------------------------------------------------------------------------

// SecretAttributes holds metadata about a secret version.
type SecretAttributes struct {
	Enabled       *bool  `json:"enabled,omitempty"`
	Created       int64  `json:"created,omitempty"`
	Updated       int64  `json:"updated,omitempty"`
	RecoveryLevel string `json:"recoveryLevel,omitempty"`
}

// SecretBundle is the full representation of a secret (value + metadata).
type SecretBundle struct {
	Value       string            `json:"value,omitempty"`
	ID          string            `json:"id,omitempty"`
	Attributes  *SecretAttributes `json:"attributes,omitempty"`
	ContentType string            `json:"contentType,omitempty"`
	Tags        map[string]string `json:"tags,omitempty"`
}

// SecretItem is the abbreviated form used in list responses (no value).
type SecretItem struct {
	ID          string            `json:"id,omitempty"`
	Attributes  *SecretAttributes `json:"attributes,omitempty"`
	ContentType string            `json:"contentType,omitempty"`
	Tags        map[string]string `json:"tags,omitempty"`
}

// DeletedSecretBundle extends SecretBundle with soft-delete fields.
type DeletedSecretBundle struct {
	Value              string            `json:"value,omitempty"`
	ID                 string            `json:"id,omitempty"`
	Attributes         *SecretAttributes `json:"attributes,omitempty"`
	ContentType        string            `json:"contentType,omitempty"`
	Tags               map[string]string `json:"tags,omitempty"`
	RecoveryID         string            `json:"recoveryId,omitempty"`
	ScheduledPurgeDate int64             `json:"scheduledPurgeDate,omitempty"`
	DeletedDate        int64             `json:"deletedDate,omitempty"`
}

// secretRecord is the internal representation stored in the backing store.
// It tracks all versions of a secret.
type secretRecord struct {
	Versions []secretVersion `json:"versions"`
}

type secretVersion struct {
	Version     string            `json:"version"`
	Value       string            `json:"value"`
	ContentType string            `json:"contentType"`
	Tags        map[string]string `json:"tags"`
	Attributes  *SecretAttributes `json:"attributes"`
}

// ---------------------------------------------------------------------------
// Request body for PUT
// ---------------------------------------------------------------------------

type setSecretRequest struct {
	Value       string            `json:"value"`
	ContentType string            `json:"contentType"`
	Attributes  *SecretAttributes `json:"attributes"`
	Tags        map[string]string `json:"tags"`
}

// ---------------------------------------------------------------------------
// RegisterRoutes wires Key Vault secret endpoints into the given mux.
// ---------------------------------------------------------------------------

// RegisterRoutes registers Azure Key Vault secret API routes on mux.
func RegisterRoutes(mux *http.ServeMux, store storage.Store, cfg *config.Config) {
	h := &handler{store: store, cfg: cfg}

	// PUT /secrets/{secretName}          — set secret
	// GET /secrets/{secretName}/{version} — get specific version
	// GET /secrets/{secretName}           — get latest version
	// GET /secrets                        — list secrets
	// DELETE /secrets/{secretName}        — soft delete

	mux.HandleFunc("PUT /secrets/{secretName}", h.withHeaders(h.setSecret))
	mux.HandleFunc("GET /secrets/{secretName}/{version}", h.withHeaders(h.getSecretVersion))
	mux.HandleFunc("GET /secrets/{secretName}", h.withHeaders(h.getSecret))
	// azsecrets SDK appends a trailing slash: GET /secrets/{name}/ → route to getSecret
	mux.HandleFunc("GET /secrets/{secretName}/", h.withHeaders(h.getSecret))
	mux.HandleFunc("GET /secrets", h.withHeaders(h.listSecrets))
	mux.HandleFunc("DELETE /secrets/{secretName}", h.withHeaders(h.deleteSecret))
}

// ---------------------------------------------------------------------------
// handler
// ---------------------------------------------------------------------------

type handler struct {
	store storage.Store
	cfg   *config.Config
}

// withHeaders sets common Key Vault response headers and issues a WWW-Authenticate
// bearer challenge when no Authorization header is present. The azsecrets SDK
// relies on this challenge to discover the token endpoint before retrying with auth.
func (h *handler) withHeaders(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("x-ms-request-id", server.RequestID())
		w.Header().Set("x-ms-keyvault-region", h.cfg.Azure.Region)
		w.Header().Set("Content-Type", "application/json; charset=utf-8")

		if r.Header.Get("Authorization") == "" {
			scheme := "http"
			if r.TLS != nil {
				scheme = "https"
			}
			host := r.Host
			if host == "" {
				host = fmt.Sprintf("localhost:%d", h.cfg.Port)
			}
			authURL := fmt.Sprintf("%s://%s/%s", scheme, host, h.cfg.Azure.TenantID)
			w.Header().Set("WWW-Authenticate", fmt.Sprintf(
				`Bearer authorization="%s", resource="https://vault.azure.net/"`, authURL,
			))
			w.WriteHeader(http.StatusUnauthorized)
			return
		}

		next(w, r)
	}
}

// baseURL returns the base URL for secret IDs (e.g. "http://localhost:4567").
func (h *handler) baseURL(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	host := r.Host
	if host == "" {
		host = fmt.Sprintf("localhost:%d", h.cfg.Port)
	}
	return fmt.Sprintf("%s://%s", scheme, host)
}

// ---------------------------------------------------------------------------
// PUT /secrets/{secretName}
// ---------------------------------------------------------------------------

func (h *handler) setSecret(w http.ResponseWriter, r *http.Request) {
	secretName := r.PathValue("secretName")
	if secretName == "" {
		server.AzureError(w, "BadParameter", "Secret name is required", http.StatusBadRequest)
		return
	}

	var req setSecretRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		server.AzureError(w, "BadParameter", "Invalid request body", http.StatusBadRequest)
		return
	}

	now := time.Now().Unix()
	version := newVersionID()

	attrs := req.Attributes
	if attrs == nil {
		enabled := true
		attrs = &SecretAttributes{Enabled: &enabled}
	}
	attrs.Created = now
	attrs.Updated = now
	if attrs.RecoveryLevel == "" {
		attrs.RecoveryLevel = "Recoverable+Purgeable"
	}

	sv := secretVersion{
		Version:     version,
		Value:       req.Value,
		ContentType: req.ContentType,
		Tags:        req.Tags,
		Attributes:  attrs,
	}

	// Load existing record or create new.
	rec := h.loadRecord(secretName)
	rec.Versions = append(rec.Versions, sv)
	h.saveRecord(secretName, rec)

	base := h.baseURL(r)
	bundle := SecretBundle{
		Value:       sv.Value,
		ID:          fmt.Sprintf("%s/secrets/%s/%s", base, secretName, version),
		Attributes:  attrs,
		ContentType: sv.ContentType,
		Tags:        sv.Tags,
	}

	server.WriteJSON(w, http.StatusOK, bundle)
}

// ---------------------------------------------------------------------------
// GET /secrets/{secretName}
// ---------------------------------------------------------------------------

func (h *handler) getSecret(w http.ResponseWriter, r *http.Request) {
	secretName := r.PathValue("secretName")
	rec := h.loadRecord(secretName)

	if len(rec.Versions) == 0 {
		server.AzureError(w, "SecretNotFound", fmt.Sprintf("Secret not found: %s", secretName), http.StatusNotFound)
		return
	}

	sv := rec.Versions[len(rec.Versions)-1]
	base := h.baseURL(r)
	bundle := SecretBundle{
		Value:       sv.Value,
		ID:          fmt.Sprintf("%s/secrets/%s/%s", base, secretName, sv.Version),
		Attributes:  sv.Attributes,
		ContentType: sv.ContentType,
		Tags:        sv.Tags,
	}

	server.WriteJSON(w, http.StatusOK, bundle)
}

// ---------------------------------------------------------------------------
// GET /secrets/{secretName}/{version}
// ---------------------------------------------------------------------------

func (h *handler) getSecretVersion(w http.ResponseWriter, r *http.Request) {
	secretName := r.PathValue("secretName")
	version := r.PathValue("version")

	rec := h.loadRecord(secretName)
	for _, sv := range rec.Versions {
		if sv.Version == version {
			base := h.baseURL(r)
			bundle := SecretBundle{
				Value:       sv.Value,
				ID:          fmt.Sprintf("%s/secrets/%s/%s", base, secretName, version),
				Attributes:  sv.Attributes,
				ContentType: sv.ContentType,
				Tags:        sv.Tags,
			}
			server.WriteJSON(w, http.StatusOK, bundle)
			return
		}
	}

	server.AzureError(w, "SecretNotFound",
		fmt.Sprintf("Secret version not found: %s/%s", secretName, version),
		http.StatusNotFound)
}

// ---------------------------------------------------------------------------
// GET /secrets
// ---------------------------------------------------------------------------

func (h *handler) listSecrets(w http.ResponseWriter, r *http.Request) {
	maxResults := 25
	if v := r.URL.Query().Get("maxresults"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			maxResults = n
		}
	}

	keys := h.store.List(namespace, "")

	// Sort for deterministic output.
	sort.Strings(keys)

	base := h.baseURL(r)
	var items []SecretItem
	for _, key := range keys {
		if len(items) >= maxResults {
			break
		}
		rec := h.loadRecord(key)
		if len(rec.Versions) == 0 {
			continue
		}
		latest := rec.Versions[len(rec.Versions)-1]
		items = append(items, SecretItem{
			ID:          fmt.Sprintf("%s/secrets/%s", base, key),
			Attributes:  latest.Attributes,
			ContentType: latest.ContentType,
			Tags:        latest.Tags,
		})
	}

	type listResponse struct {
		Value    []SecretItem `json:"value"`
		NextLink *string      `json:"nextLink"`
	}

	server.WriteJSON(w, http.StatusOK, listResponse{
		Value:    items,
		NextLink: nil,
	})
}

// ---------------------------------------------------------------------------
// DELETE /secrets/{secretName}
// ---------------------------------------------------------------------------

func (h *handler) deleteSecret(w http.ResponseWriter, r *http.Request) {
	secretName := r.PathValue("secretName")
	rec := h.loadRecord(secretName)

	if len(rec.Versions) == 0 {
		server.AzureError(w, "SecretNotFound", fmt.Sprintf("Secret not found: %s", secretName), http.StatusNotFound)
		return
	}

	latest := rec.Versions[len(rec.Versions)-1]
	now := time.Now()
	purge := now.Add(90 * 24 * time.Hour)
	base := h.baseURL(r)

	deleted := DeletedSecretBundle{
		Value:              latest.Value,
		ID:                 fmt.Sprintf("%s/secrets/%s/%s", base, secretName, latest.Version),
		Attributes:         latest.Attributes,
		ContentType:        latest.ContentType,
		Tags:               latest.Tags,
		RecoveryID:         fmt.Sprintf("%s/deletedsecrets/%s", base, secretName),
		ScheduledPurgeDate: purge.Unix(),
		DeletedDate:        now.Unix(),
	}

	h.store.Delete(namespace, secretName)

	server.WriteJSON(w, http.StatusOK, deleted)
}

// ---------------------------------------------------------------------------
// Storage helpers
// ---------------------------------------------------------------------------

func (h *handler) loadRecord(secretName string) *secretRecord {
	data, ok := h.store.Get(namespace, secretName)
	if !ok {
		return &secretRecord{}
	}
	var rec secretRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return &secretRecord{}
	}
	return &rec
}

func (h *handler) saveRecord(secretName string, rec *secretRecord) {
	data, _ := json.Marshal(rec)
	h.store.Put(namespace, secretName, data)
}

// newVersionID generates a hex UUID suitable for a secret version identifier.
func newVersionID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("%x%x%x%x%x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

