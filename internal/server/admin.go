package server

import (
	_ "embed"
	"encoding/json"
	"net/http"
	"sort"

	"github.com/altocirrus/altocirrus/internal/storage"
)

//go:embed admin.html
var adminHTML []byte

// RegisterAdminRoutes adds the admin dashboard and API endpoints.
func RegisterAdminRoutes(mux *http.ServeMux, store storage.Store) {
	mux.HandleFunc("GET /_altocirrus/admin", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(adminHTML)
	})

	mux.HandleFunc("GET /_altocirrus/api/namespaces", func(w http.ResponseWriter, r *http.Request) {
		ns := store.Namespaces()
		sort.Strings(ns)

		type nsInfo struct {
			Namespace string `json:"namespace"`
			Count     int    `json:"count"`
		}

		result := make([]nsInfo, 0, len(ns))
		for _, n := range ns {
			keys := store.List(n, "")
			result = append(result, nsInfo{Namespace: n, Count: len(keys)})
		}
		WriteJSON(w, http.StatusOK, result)
	})

	mux.HandleFunc("GET /_altocirrus/api/namespaces/{ns}/keys", func(w http.ResponseWriter, r *http.Request) {
		ns := r.PathValue("ns")
		keys := store.List(ns, "")
		sort.Strings(keys)
		if keys == nil {
			keys = []string{}
		}
		WriteJSON(w, http.StatusOK, keys)
	})

	mux.HandleFunc("GET /_altocirrus/api/namespaces/{ns}/keys/{key...}", func(w http.ResponseWriter, r *http.Request) {
		ns := r.PathValue("ns")
		key := r.PathValue("key")
		raw, ok := store.Get(ns, key)
		if !ok {
			WriteJSON(w, http.StatusNotFound, map[string]string{"error": "key not found"})
			return
		}
		// Try to return as parsed JSON; fall back to raw string
		var parsed any
		if json.Unmarshal(raw, &parsed) == nil {
			WriteJSON(w, http.StatusOK, parsed)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Write(raw)
	})
}
