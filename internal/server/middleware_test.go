package server_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/altocirrus/altocirrus/internal/server"
)

// dummyHandler is a simple handler that writes a 200 status.
func dummyHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
}

// statusHandler writes the given status code to test logging captures it.
func statusHandler(code int) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(code)
	})
}

func TestCORSMiddleware_OPTIONS(t *testing.T) {
	handler := server.CORSMiddleware(dummyHandler())

	req := httptest.NewRequest(http.MethodOptions, "/some/path", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Errorf("OPTIONS status = %d, want %d", rec.Code, http.StatusNoContent)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Errorf("Allow-Origin = %q, want *", got)
	}
	if got := rec.Header().Get("Access-Control-Allow-Methods"); got == "" {
		t.Error("Allow-Methods header is empty")
	}
	if got := rec.Header().Get("Access-Control-Allow-Headers"); got == "" {
		t.Error("Allow-Headers header is empty")
	}
	if got := rec.Header().Get("Access-Control-Max-Age"); got != "86400" {
		t.Errorf("Max-Age = %q, want 86400", got)
	}
}

func TestCORSMiddleware_GET(t *testing.T) {
	handler := server.CORSMiddleware(dummyHandler())

	req := httptest.NewRequest(http.MethodGet, "/some/path", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("GET status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Errorf("Allow-Origin = %q, want *", got)
	}
	// Non-preflight requests should not have the full CORS method list.
	if got := rec.Header().Get("Access-Control-Allow-Methods"); got != "" {
		t.Errorf("Allow-Methods should be empty on non-OPTIONS, got %q", got)
	}
}

func TestLoggingMiddleware_CapturesStatusCode(t *testing.T) {
	handler := server.LoggingMiddleware(statusHandler(http.StatusCreated))

	req := httptest.NewRequest(http.MethodPost, "/test", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	// The underlying handler wrote 201; verify it reaches the client.
	if rec.Code != http.StatusCreated {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusCreated)
	}
}

func TestLoggingMiddleware_SetsAzureRequestID(t *testing.T) {
	handler := server.LoggingMiddleware(dummyHandler())

	req := httptest.NewRequest(http.MethodGet, "/subscriptions/abc/resourceGroups", nil)
	req.Header.Set("x-ms-client-request-id", "client-id-123")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if got := rec.Header().Get("x-ms-request-id"); got != "client-id-123" {
		t.Errorf("x-ms-request-id = %q, want client-id-123", got)
	}
}

func TestLoggingMiddleware_SetsGCPRequestID(t *testing.T) {
	handler := server.LoggingMiddleware(dummyHandler())

	req := httptest.NewRequest(http.MethodGet, "/v1/projects/my-proj/secrets", nil)
	req.Header.Set("x-goog-request-id", "gcp-id-456")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if got := rec.Header().Get("x-goog-request-id"); got != "gcp-id-456" {
		t.Errorf("x-goog-request-id = %q, want gcp-id-456", got)
	}
}

func TestLoggingMiddleware_GeneratesRequestID(t *testing.T) {
	handler := server.LoggingMiddleware(dummyHandler())

	// No request ID headers set; middleware should generate one.
	req := httptest.NewRequest(http.MethodGet, "/subscriptions/abc", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if got := rec.Header().Get("x-ms-request-id"); got == "" {
		t.Error("expected generated x-ms-request-id, got empty")
	}
}

func TestMiddlewareChain(t *testing.T) {
	inner := dummyHandler()
	handler := server.LoggingMiddleware(server.CORSMiddleware(inner))

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Errorf("Allow-Origin = %q, want *", got)
	}
}
