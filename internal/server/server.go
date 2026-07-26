package server

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/altocirrus/altocirrus/internal/config"
	"github.com/altocirrus/altocirrus/internal/storage"
)

// knownNamespaces lists all storage namespaces used by emulated services.
// Add new namespaces here as services are implemented.
var knownNamespaces = []string{
	"azure:keyvault",
	"azure:arm",
	"azure:auth",
	"azure:blob:containers",
	"azure:queuestorage:queues",
	"azure:cosmosdb:dbs",
	"azure:cosmosdb:colls",
	"azure:cosmosdb:docs",
	"gcp:secretmanager",
	"gcp:storage",
	"gcp:auth",
	"gcp:pubsub:topics",
	"gcp:pubsub:subscriptions",
	"gcp:firestore:docs",
}

// New creates a configured HTTP mux with health and reset endpoints.
// Service-specific routes will be added in later phases.
func New(cfg *config.Config, store storage.Store) *http.ServeMux {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /_altocirrus/health", withLogging(handleHealth()))
	mux.HandleFunc("POST /_altocirrus/reset", withLogging(handleReset(store)))

	return mux
}

// handleHealth returns the health check handler.
func handleHealth() http.HandlerFunc {
	type healthResponse struct {
		Status   string              `json:"status"`
		Version  string              `json:"version"`
		Services map[string][]string `json:"services"`
	}

	resp := healthResponse{
		Status:  "ok",
		Version: "0.1.0",
		Services: map[string][]string{
			"azure": {"auth", "keyvault", "arm", "blobstorage", "queuestorage", "cosmosdb"},
			"gcp":   {"auth", "secretmanager", "storage", "pubsub", "firestore"},
		},
	}

	return func(w http.ResponseWriter, r *http.Request) {
		WriteJSON(w, http.StatusOK, resp)
	}
}

// handleReset returns the reset handler, which clears all known namespaces.
func handleReset(store storage.Store) http.HandlerFunc {
	type resetResponse struct {
		Status string `json:"status"`
	}

	return func(w http.ResponseWriter, r *http.Request) {
		for _, ns := range knownNamespaces {
			store.Clear(ns)
		}
		WriteJSON(w, http.StatusOK, resetResponse{Status: "reset"})
	}
}

// withLogging wraps an http.HandlerFunc with request logging.
func withLogging(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		lw := &loggingResponseWriter{ResponseWriter: w, statusCode: http.StatusOK}
		next(lw, r)

		slog.Info("request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", lw.statusCode,
			"duration_ms", time.Since(start).Milliseconds(),
		)
	}
}

// loggingResponseWriter wraps http.ResponseWriter to capture the status code.
type loggingResponseWriter struct {
	http.ResponseWriter
	statusCode int
}

func (lw *loggingResponseWriter) WriteHeader(code int) {
	lw.statusCode = code
	lw.ResponseWriter.WriteHeader(code)
}
