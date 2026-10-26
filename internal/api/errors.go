package api

import (
	"encoding/json"
	"net/http"
)

// Kind is the E-channel tag from docs/01-architecture.md. It travels in the
// OpenAI error "type" field so clients and logs classify by tag, never by
// message text. M0 can produce the first two; internal/connector adds the
// upstream kinds in M1/M2.
type Kind string

const (
	KindBadRequest  Kind = "BadRequest"
	KindUnsupported Kind = "Unsupported"
	KindAuth        Kind = "Auth"
)

type errorEnvelope struct {
	Error errorDetail `json:"error"`
}

type errorDetail struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Param   string `json:"param,omitempty"`
	Code    string `json:"code"`
}

func writeError(w http.ResponseWriter, status int, kind Kind, code, message string) {
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
