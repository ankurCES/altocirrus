package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/altocirrus/altocirrus/internal/storage"
)

func TestAdminNamespaces(t *testing.T) {
	store := storage.NewMemoryStore()
	store.Put("ns1", "a", []byte(`"val"`))
	store.Put("ns1", "b", []byte(`"val2"`))
	store.Put("ns2", "x", []byte(`"y"`))

	mux := http.NewServeMux()
	RegisterAdminRoutes(mux, store)

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/_altocirrus/api/namespaces", nil))
	if w.Code != 200 {
		t.Fatalf("want 200, got %d", w.Code)
	}
	var result []struct {
		Namespace string `json:"namespace"`
		Count     int    `json:"count"`
	}
	json.Unmarshal(w.Body.Bytes(), &result)
	if len(result) != 2 {
		t.Fatalf("want 2 namespaces, got %d", len(result))
	}
}

func TestAdminKeys(t *testing.T) {
	store := storage.NewMemoryStore()
	store.Put("test-ns", "key1", []byte(`{"hello":"world"}`))

	mux := http.NewServeMux()
	RegisterAdminRoutes(mux, store)

	// List keys
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/_altocirrus/api/namespaces/test-ns/keys", nil))
	if w.Code != 200 {
		t.Fatalf("list keys: want 200, got %d", w.Code)
	}
	var keys []string
	json.Unmarshal(w.Body.Bytes(), &keys)
	if len(keys) != 1 || keys[0] != "key1" {
		t.Fatalf("keys: want [key1], got %v", keys)
	}

	// Get value
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/_altocirrus/api/namespaces/test-ns/keys/key1", nil))
	if w.Code != 200 {
		t.Fatalf("get key: want 200, got %d", w.Code)
	}

	// Not found
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/_altocirrus/api/namespaces/test-ns/keys/nope", nil))
	if w.Code != 404 {
		t.Fatalf("missing key: want 404, got %d", w.Code)
	}
}

func TestAdminDashboard(t *testing.T) {
	store := storage.NewMemoryStore()
	mux := http.NewServeMux()
	RegisterAdminRoutes(mux, store)

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/_altocirrus/admin", nil))
	if w.Code != 200 {
		t.Fatalf("admin page: want 200, got %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Fatalf("content-type: want text/html, got %s", ct)
	}
}
