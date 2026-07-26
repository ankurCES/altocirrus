package server

import (
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// CORSMiddleware handles CORS preflight and sets Access-Control-Allow-Origin
// on all responses. OPTIONS requests are short-circuited with 204 No Content.
func CORSMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")

		if r.Method == http.MethodOptions {
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, PATCH, HEAD, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, x-ms-version, x-ms-client-request-id, x-goog-request-id, api-version")
			w.Header().Set("Access-Control-Max-Age", "86400")
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// LoggingMiddleware logs structured information about every request including
// method, path, status code, duration, and a request ID. The request ID is
// extracted from x-ms-client-request-id or x-goog-request-id headers; if
// neither is present a new UUID is generated. The resolved request ID is
// returned as x-ms-request-id (Azure paths) or x-goog-request-id (GCP paths).
func LoggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		// Extract or generate request ID.
		reqID := r.Header.Get("x-ms-client-request-id")
		if reqID == "" {
			reqID = r.Header.Get("x-goog-request-id")
		}
		if reqID == "" {
			reqID = RequestID()
		}

		// Set request ID on response based on path prefix.
		if isGCPPath(r.URL.Path) {
			w.Header().Set("x-goog-request-id", reqID)
		} else {
			w.Header().Set("x-ms-request-id", reqID)
		}

		lw := &loggingResponseWriter{ResponseWriter: w, statusCode: http.StatusOK}
		next.ServeHTTP(lw, r)

		slog.Info("request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", lw.statusCode,
			"duration_ms", time.Since(start).Milliseconds(),
			"request_id", reqID,
		)
	})
}

// isGCPPath returns true if the path looks like a GCP API path.
func isGCPPath(path string) bool {
	return strings.HasPrefix(path, "/v1/projects/") ||
		strings.HasPrefix(path, "/token") ||
		strings.HasPrefix(path, "/v1/b/") ||
		strings.HasPrefix(path, "/upload/storage/") ||
		strings.HasPrefix(path, "/storage/")
}
