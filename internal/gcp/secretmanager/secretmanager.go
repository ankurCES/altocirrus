package secretmanager

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/altocirrus/altocirrus/internal/config"
	"github.com/altocirrus/altocirrus/internal/server"
	"github.com/altocirrus/altocirrus/internal/storage"
)

const namespace = "gcp:secretmanager"

// ---------------------------------------------------------------------------
// Models
// ---------------------------------------------------------------------------

// Secret represents a GCP Secret Manager secret resource.
type Secret struct {
	Name        string            `json:"name"`
	Replication *Replication      `json:"replication,omitempty"`
	CreateTime  string            `json:"createTime"`
	Etag        string            `json:"etag"`
	Labels      map[string]string `json:"labels,omitempty"`
}

// Replication describes the secret replication policy.
type Replication struct {
	Automatic *AutomaticReplication `json:"automatic,omitempty"`
}

// AutomaticReplication is the automatic replication config (empty object).
type AutomaticReplication struct{}

// SecretVersion represents a single version of a secret.
type SecretVersion struct {
	Name       string `json:"name"`
	CreateTime string `json:"createTime"`
	State      string `json:"state"`
	Etag       string `json:"etag"`
}

// AccessResponse is returned when accessing a secret version's payload.
type AccessResponse struct {
	Name    string          `json:"name"`
	Payload *SecretPayload  `json:"payload"`
}

// SecretPayload holds the base64-encoded data and its CRC32C checksum.
type SecretPayload struct {
	Data      string `json:"data"`
	DataCrc32c string `json:"dataCrc32c"`
}

// ---------------------------------------------------------------------------
// Internal stored representation
// ---------------------------------------------------------------------------

// storedSecret is the internal representation persisted in the store.
type storedSecret struct {
	Project     string            `json:"project"`
	SecretID    string            `json:"secretId"`
	Replication *Replication      `json:"replication,omitempty"`
	CreateTime  string            `json:"createTime"`
	Etag        string            `json:"etag"`
	Labels      map[string]string `json:"labels,omitempty"`
	Versions    []storedVersion   `json:"versions"`
}

// storedVersion is the internal representation of a secret version.
type storedVersion struct {
	Number     int    `json:"number"`
	Data       string `json:"data"` // base64-encoded
	CreateTime string `json:"createTime"`
	State      string `json:"state"`
	Etag       string `json:"etag"`
}

// ---------------------------------------------------------------------------
// Route registration
// ---------------------------------------------------------------------------

// RegisterRoutes registers all GCP Secret Manager emulation routes on mux.
func RegisterRoutes(mux *http.ServeMux, store storage.Store, cfg *config.Config) {
	h := &handler{store: store, cfg: cfg}

	// Use a catch-all pattern because GCP Secret Manager paths contain colons
	// (:addVersion, :access) which do not work well with ServeMux patterns.
	mux.HandleFunc("/v1/projects/", h.route)
}

type handler struct {
	store storage.Store
	cfg   *config.Config
}

// route dispatches incoming requests by parsing the URL path manually.
func (h *handler) route(w http.ResponseWriter, r *http.Request) {
	// Strip the leading "/v1/projects/" prefix.
	path := strings.TrimPrefix(r.URL.Path, "/v1/projects/")

	// Split into segments: path = "{project}/secrets/..." or similar
	parts := strings.SplitN(path, "/", 2)
	if len(parts) < 2 {
		server.GCPError(w, http.StatusNotFound, "not found", "NOT_FOUND")
		return
	}
	project := parts[0]
	rest := parts[1] // "secrets/..." or "secrets"

	// Must start with "secrets"
	if !strings.HasPrefix(rest, "secrets") {
		server.GCPError(w, http.StatusNotFound, "not found", "NOT_FOUND")
		return
	}

	sub := strings.TrimPrefix(rest, "secrets")

	switch {
	// POST /v1/projects/{project}/secrets?secretId=...  (create)
	// GET  /v1/projects/{project}/secrets               (list)
	case sub == "" || sub == "/":
		switch r.Method {
		case http.MethodPost:
			h.createSecret(w, r, project)
		case http.MethodGet:
			h.listSecrets(w, r, project)
		default:
			server.GCPError(w, http.StatusMethodNotAllowed, "method not allowed", "INVALID_ARGUMENT")
		}

	default:
		// sub starts with "/"
		// Possible shapes:
		//   /{secret}
		//   /{secret}:addVersion
		//   /{secret}/versions/{version}:access
		sub = strings.TrimPrefix(sub, "/")
		h.routeSecret(w, r, project, sub)
	}
}

// routeSecret handles paths after /v1/projects/{project}/secrets/
func (h *handler) routeSecret(w http.ResponseWriter, r *http.Request, project, sub string) {
	// Check for :addVersion
	if colonIdx := strings.Index(sub, ":"); colonIdx >= 0 && !strings.Contains(sub[:colonIdx], "/") {
		secretID := sub[:colonIdx]
		action := sub[colonIdx+1:]
		if action == "addVersion" && r.Method == http.MethodPost {
			h.addVersion(w, r, project, secretID)
			return
		}
		server.GCPError(w, http.StatusNotFound, "not found", "NOT_FOUND")
		return
	}

	// Check for versions path: {secret}/versions/{version}:access
	if idx := strings.Index(sub, "/versions/"); idx >= 0 {
		secretID := sub[:idx]
		versionPart := sub[idx+len("/versions/"):]

		// Check for :access suffix
		if strings.HasSuffix(versionPart, ":access") {
			version := strings.TrimSuffix(versionPart, ":access")
			if r.Method == http.MethodGet {
				h.accessVersion(w, r, project, secretID, version)
				return
			}
			server.GCPError(w, http.StatusMethodNotAllowed, "method not allowed", "INVALID_ARGUMENT")
			return
		}

		server.GCPError(w, http.StatusNotFound, "not found", "NOT_FOUND")
		return
	}

	// Plain /{secret} — get or delete
	secretID := sub
	switch r.Method {
	case http.MethodGet:
		h.getSecret(w, r, project, secretID)
	case http.MethodDelete:
		h.deleteSecret(w, r, project, secretID)
	default:
		server.GCPError(w, http.StatusMethodNotAllowed, "method not allowed", "INVALID_ARGUMENT")
	}
}

// ---------------------------------------------------------------------------
// Endpoint handlers
// ---------------------------------------------------------------------------

// createSecret handles POST /v1/projects/{project}/secrets?secretId={name}
func (h *handler) createSecret(w http.ResponseWriter, r *http.Request, project string) {
	secretID := r.URL.Query().Get("secretId")
	if secretID == "" {
		server.GCPError(w, http.StatusBadRequest, "secretId query parameter is required", "INVALID_ARGUMENT")
		return
	}

	key := project + "/" + secretID

	// Check for conflict.
	if _, exists := h.store.Get(namespace, key); exists {
		server.GCPError(w, http.StatusConflict,
			fmt.Sprintf("Secret [projects/%s/secrets/%s] already exists.", project, secretID),
			"ALREADY_EXISTS")
		return
	}

	// Parse request body.
	var body struct {
		Replication *Replication       `json:"replication"`
		Labels      map[string]string  `json:"labels"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		server.GCPError(w, http.StatusBadRequest, "invalid JSON body", "INVALID_ARGUMENT")
		return
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	etag := server.RequestID()

	stored := storedSecret{
		Project:     project,
		SecretID:    secretID,
		Replication: body.Replication,
		CreateTime:  now,
		Etag:        etag,
		Labels:      body.Labels,
		Versions:    nil,
	}

	data, _ := json.Marshal(stored)
	h.store.Put(namespace, key, data)

	resp := Secret{
		Name:        fmt.Sprintf("projects/%s/secrets/%s", project, secretID),
		Replication: stored.Replication,
		CreateTime:  now,
		Etag:        etag,
		Labels:      stored.Labels,
	}

	server.WriteJSON(w, http.StatusOK, resp)
}

// addVersion handles POST /v1/projects/{project}/secrets/{secret}:addVersion
func (h *handler) addVersion(w http.ResponseWriter, r *http.Request, project, secretID string) {
	key := project + "/" + secretID

	raw, exists := h.store.Get(namespace, key)
	if !exists {
		server.GCPError(w, http.StatusNotFound,
			fmt.Sprintf("Secret [projects/%s/secrets/%s] not found.", project, secretID),
			"NOT_FOUND")
		return
	}

	var stored storedSecret
	if err := json.Unmarshal(raw, &stored); err != nil {
		server.GCPError(w, http.StatusInternalServerError, "internal error", "INTERNAL")
		return
	}

	var body struct {
		Payload struct {
			Data string `json:"data"`
		} `json:"payload"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		server.GCPError(w, http.StatusBadRequest, "invalid JSON body", "INVALID_ARGUMENT")
		return
	}

	// Validate base64.
	if _, err := base64.StdEncoding.DecodeString(body.Payload.Data); err != nil {
		server.GCPError(w, http.StatusBadRequest, "payload data is not valid base64", "INVALID_ARGUMENT")
		return
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	versionNum := len(stored.Versions) + 1
	etag := server.RequestID()

	ver := storedVersion{
		Number:     versionNum,
		Data:       body.Payload.Data,
		CreateTime: now,
		State:      "ENABLED",
		Etag:       etag,
	}
	stored.Versions = append(stored.Versions, ver)

	data, _ := json.Marshal(stored)
	h.store.Put(namespace, key, data)

	resp := SecretVersion{
		Name:       fmt.Sprintf("projects/%s/secrets/%s/versions/%d", project, secretID, versionNum),
		CreateTime: now,
		State:      "ENABLED",
		Etag:       etag,
	}

	server.WriteJSON(w, http.StatusOK, resp)
}

// accessVersion handles GET /v1/projects/{project}/secrets/{secret}/versions/{version}:access
func (h *handler) accessVersion(w http.ResponseWriter, _ *http.Request, project, secretID, version string) {
	key := project + "/" + secretID

	raw, exists := h.store.Get(namespace, key)
	if !exists {
		server.GCPError(w, http.StatusNotFound,
			fmt.Sprintf("Secret [projects/%s/secrets/%s] not found.", project, secretID),
			"NOT_FOUND")
		return
	}

	var stored storedSecret
	if err := json.Unmarshal(raw, &stored); err != nil {
		server.GCPError(w, http.StatusInternalServerError, "internal error", "INTERNAL")
		return
	}

	if len(stored.Versions) == 0 {
		server.GCPError(w, http.StatusNotFound,
			fmt.Sprintf("Secret version [projects/%s/secrets/%s/versions/%s] not found or has no versions.",
				project, secretID, version),
			"NOT_FOUND")
		return
	}

	var ver storedVersion
	var versionLabel string

	if version == "latest" {
		ver = stored.Versions[len(stored.Versions)-1]
		versionLabel = strconv.Itoa(ver.Number)
	} else {
		n, err := strconv.Atoi(version)
		if err != nil || n < 1 || n > len(stored.Versions) {
			server.GCPError(w, http.StatusNotFound,
				fmt.Sprintf("Secret version [projects/%s/secrets/%s/versions/%s] not found.",
					project, secretID, version),
				"NOT_FOUND")
			return
		}
		ver = stored.Versions[n-1]
		versionLabel = version
	}

	// Decode the base64 data to compute CRC32C.
	decoded, _ := base64.StdEncoding.DecodeString(ver.Data)
	crc := crc32.Checksum(decoded, crc32.MakeTable(crc32.Castagnoli))

	resp := AccessResponse{
		Name: fmt.Sprintf("projects/%s/secrets/%s/versions/%s", project, secretID, versionLabel),
		Payload: &SecretPayload{
			Data:       ver.Data,
			DataCrc32c: strconv.FormatUint(uint64(crc), 10),
		},
	}

	server.WriteJSON(w, http.StatusOK, resp)
}

// listSecrets handles GET /v1/projects/{project}/secrets
func (h *handler) listSecrets(w http.ResponseWriter, r *http.Request, project string) {
	pageSizeStr := r.URL.Query().Get("pageSize")
	pageSize := 25
	if pageSizeStr != "" {
		if n, err := strconv.Atoi(pageSizeStr); err == nil && n > 0 {
			pageSize = n
		}
	}

	prefix := project + "/"
	keys := h.store.List(namespace, prefix)
	sort.Strings(keys)

	var secrets []Secret
	for _, k := range keys {
		raw, ok := h.store.Get(namespace, k)
		if !ok {
			continue
		}
		var stored storedSecret
		if err := json.Unmarshal(raw, &stored); err != nil {
			continue
		}
		secrets = append(secrets, Secret{
			Name:        fmt.Sprintf("projects/%s/secrets/%s", stored.Project, stored.SecretID),
			Replication: stored.Replication,
			CreateTime:  stored.CreateTime,
			Etag:        stored.Etag,
			Labels:      stored.Labels,
		})
	}

	totalSize := len(secrets)

	// Apply pageSize limit.
	if len(secrets) > pageSize {
		secrets = secrets[:pageSize]
	}

	type listResponse struct {
		Secrets   []Secret `json:"secrets"`
		TotalSize int      `json:"totalSize"`
	}

	resp := listResponse{
		Secrets:   secrets,
		TotalSize: totalSize,
	}

	// Ensure we return an empty array, not null.
	if resp.Secrets == nil {
		resp.Secrets = []Secret{}
	}

	server.WriteJSON(w, http.StatusOK, resp)
}

// getSecret handles GET /v1/projects/{project}/secrets/{secret}
func (h *handler) getSecret(w http.ResponseWriter, _ *http.Request, project, secretID string) {
	key := project + "/" + secretID

	raw, exists := h.store.Get(namespace, key)
	if !exists {
		server.GCPError(w, http.StatusNotFound,
			fmt.Sprintf("Secret [projects/%s/secrets/%s] not found.", project, secretID),
			"NOT_FOUND")
		return
	}

	var stored storedSecret
	if err := json.Unmarshal(raw, &stored); err != nil {
		server.GCPError(w, http.StatusInternalServerError, "internal error", "INTERNAL")
		return
	}

	resp := Secret{
		Name:        fmt.Sprintf("projects/%s/secrets/%s", project, secretID),
		Replication: stored.Replication,
		CreateTime:  stored.CreateTime,
		Etag:        stored.Etag,
		Labels:      stored.Labels,
	}

	server.WriteJSON(w, http.StatusOK, resp)
}

// deleteSecret handles DELETE /v1/projects/{project}/secrets/{secret}
func (h *handler) deleteSecret(w http.ResponseWriter, _ *http.Request, project, secretID string) {
	key := project + "/" + secretID

	if !h.store.Delete(namespace, key) {
		server.GCPError(w, http.StatusNotFound,
			fmt.Sprintf("Secret [projects/%s/secrets/%s] not found.", project, secretID),
			"NOT_FOUND")
		return
	}

	server.WriteJSON(w, http.StatusOK, map[string]any{})
}
