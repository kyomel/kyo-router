package api

import (
	"encoding/json"
	"net/http"

	"kyo-router/internal/connector"
)

type errorEnvelope struct {
	Error errorDetail `json:"error"`
}

type errorDetail struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    string `json:"code"`
}

// writeError answers one error in the OpenAI shape. The kind travels in the
// "type" field so clients and logs classify by tag, never by message text.
func writeError(w http.ResponseWriter, status int, kind connector.Kind, code, message string) {
	writeJSON(w, status, errorEnvelope{Error: errorDetail{
		Message: message,
		Type:    string(kind),
		Code:    code,
	}})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
