package server

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
)

// WriteJSON marshals v as JSON and writes it to w with the given HTTP status.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)

	if err := json.NewEncoder(w).Encode(v); err != nil {
		// At this point headers are sent; log and move on.
		http.Error(w, `{"error":"internal encoding failure"}`, http.StatusInternalServerError)
	}
}

// AzureError writes an Azure ARM-style error response envelope.
//
//	{
//	  "error": {
//	    "code": "<code>",
//	    "message": "<message>"
//	  }
//	}
func AzureError(w http.ResponseWriter, code string, message string, status int) {
	type azureErrorDetail struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	type azureErrorEnvelope struct {
		Error azureErrorDetail `json:"error"`
	}

	w.Header().Set("x-ms-request-id", RequestID())
	WriteJSON(w, status, azureErrorEnvelope{
		Error: azureErrorDetail{
			Code:    code,
			Message: message,
		},
	})
}

// GCPError writes a Google API-style error response envelope.
//
//	{
//	  "error": {
//	    "code": <code>,
//	    "message": "<message>",
//	    "status": "<status>"
//	  }
//	}
func GCPError(w http.ResponseWriter, code int, message string, gcpStatus string) {
	type gcpErrorDetail struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Status  string `json:"status"`
	}
	type gcpErrorEnvelope struct {
		Error gcpErrorDetail `json:"error"`
	}

	WriteJSON(w, code, gcpErrorEnvelope{
		Error: gcpErrorDetail{
			Code:    code,
			Message: message,
			Status:  gcpStatus,
		},
	})
}

// RequestID generates a new UUID v4 string using crypto/rand.
func RequestID() string {
	var uuid [16]byte
	_, err := rand.Read(uuid[:])
	if err != nil {
		// Fallback: should never happen with crypto/rand on a modern OS.
		return "00000000-0000-0000-0000-000000000000"
	}
	// Set version (4) and variant (RFC 4122) bits.
	uuid[6] = (uuid[6] & 0x0f) | 0x40
	uuid[8] = (uuid[8] & 0x3f) | 0x80

	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		uuid[0:4], uuid[4:6], uuid[6:8], uuid[8:10], uuid[10:16])
}
