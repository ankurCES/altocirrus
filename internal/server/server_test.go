package server_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/altocirrus/altocirrus/internal/config"
	"github.com/altocirrus/altocirrus/internal/server"
	"github.com/altocirrus/altocirrus/internal/storage"
)

func TestWriteJSON(t *testing.T) {
	w := httptest.NewRecorder()
	server.WriteJSON(w, http.StatusCreated, map[string]string{"k": "v"})
	if w.Code != 201 {
		t.Errorf("status = %d, want 201", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q", ct)
	}
}

func TestAzureError(t *testing.T) {
	w := httptest.NewRecorder()
	server.AzureError(w, "ResourceNotFound", "not found", http.StatusNotFound)
	if w.Code != 404 {
		t.Errorf("status = %d, want 404", w.Code)
	}
	var body struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Error.Code != "ResourceNotFound" {
		t.Errorf("error.code = %q, want ResourceNotFound", body.Error.Code)
	}
	if w.Header().Get("x-ms-request-id") == "" {
		t.Error("x-ms-request-id header not set")
	}
}

func TestGCPError(t *testing.T) {
	w := httptest.NewRecorder()
	server.GCPError(w, http.StatusNotFound, "not found", "NOT_FOUND")
	if w.Code != 404 {
		t.Errorf("status = %d, want 404", w.Code)
	}
	var body struct {
		Error struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
			Status  string `json:"status"`
		} `json:"error"`
	}
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Error.Status != "NOT_FOUND" {
		t.Errorf("error.status = %q, want NOT_FOUND", body.Error.Status)
	}
	if body.Error.Code != 404 {
		t.Errorf("error.code = %d, want 404", body.Error.Code)
	}
}

func TestRequestID(t *testing.T) {
	id := server.RequestID()
	// UUID v4: xxxxxxxx-xxxx-4xxx-yxxx-xxxxxxxxxxxx (36 chars)
	if len(id) != 36 {
		t.Errorf("len = %d, want 36", len(id))
	}
	if id[8] != '-' || id[13] != '-' || id[18] != '-' || id[23] != '-' {
		t.Errorf("bad UUID format: %s", id)
	}
	// Version bits: char at index 14 should be '4'
	if id[14] != '4' {
		t.Errorf("version nibble = %c, want 4", id[14])
	}
}

func TestRequestIDUnique(t *testing.T) {
	a, b := server.RequestID(), server.RequestID()
	if a == b {
		t.Error("two RequestID calls returned the same value")
	}
}

func TestHealthEndpoint(t *testing.T) {
	mux := server.New(&config.Config{Port: 4567}, storage.NewMemoryStore())
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/_altocirrus/health", nil))
	if w.Code != 200 {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var body struct {
		Status   string              `json:"status"`
		Version  string              `json:"version"`
		Services map[string][]string `json:"services"`
	}
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Status != "ok" {
		t.Errorf("status = %q, want ok", body.Status)
	}
	if body.Version == "" {
		t.Error("version is empty")
	}
	if len(body.Services["azure"]) == 0 {
		t.Error("no azure services listed")
	}
	if len(body.Services["gcp"]) == 0 {
		t.Error("no gcp services listed")
	}
}

func TestResetEndpointClearsStore(t *testing.T) {
	store := storage.NewMemoryStore()
	store.Put("azure:keyvault", "secret1", []byte(`"val"`))
	store.Put("gcp:secretmanager", "proj/secrets/s", []byte(`"val"`))

	mux := server.New(&config.Config{}, store)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/_altocirrus/reset", nil))
	if w.Code != 200 {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var body struct {
		Status string `json:"status"`
	}
	json.NewDecoder(w.Body).Decode(&body)
	if body.Status != "reset" {
		t.Errorf("status = %q, want reset", body.Status)
	}

	if keys := store.List("azure:keyvault", ""); len(keys) != 0 {
		t.Errorf("azure:keyvault not cleared: %v", keys)
	}
	if keys := store.List("gcp:secretmanager", ""); len(keys) != 0 {
		t.Errorf("gcp:secretmanager not cleared: %v", keys)
	}
}
