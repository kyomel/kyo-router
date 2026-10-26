// Package api is the OpenAI-compatible HTTP surface. It parses requests at the
// boundary and answers with typed errors; no provider detail reaches it.
package api

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"kyo-router/internal/catalog"
	"kyo-router/internal/connector"
	"kyo-router/internal/models"
	"kyo-router/internal/router"
)

// Server holds what the handlers need. Build one per process.
type Server struct {
	catalog *catalog.Catalog
	router  *router.Router
	apiKey  string // optional router Bearer key; empty disables the check
	logger  *slog.Logger
}

// New returns the HTTP handler. apiKey enables client Bearer auth when set.
func New(cat *catalog.Catalog, rt *router.Router, apiKey string, logger *slog.Logger) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	s := &Server{catalog: cat, router: rt, apiKey: apiKey, logger: logger}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.healthz)
	mux.HandleFunc("GET /v1/models", s.listModels)
	mux.HandleFunc("POST /v1/chat/completions", s.chatCompletions)
	return mux
}

func (s *Server) healthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte("ok"))
}

type modelList struct {
	Object string        `json:"object"`
	Data   []modelObject `json:"data"`
}

type modelObject struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
}

func (s *Server) listModels(w http.ResponseWriter, _ *http.Request) {
	configured := s.catalog.Models()
	data := make([]modelObject, 0, len(configured))
	for _, m := range configured {
		entry := modelObject{
			Object:  "model",
			Created: s.catalog.Created(),
			OwnedBy: m.Connector,
		}
		entry.ID = m.ID
		data = append(data, entry)
		for _, alias := range m.Aliases {
			entry.ID = alias
			data = append(data, entry)
		}
	}
	writeJSON(w, http.StatusOK, modelList{Object: "list", Data: data})
}

func (s *Server) chatCompletions(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(r) {
		writeError(w, http.StatusUnauthorized, KindAuth, "unauthorized", "a valid Bearer token is required")
		return
	}
	var req models.ChatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, KindBadRequest, "invalid_json",
			"request body is not valid JSON: "+err.Error())
		return
	}
	if req.Model == "" {
		writeError(w, http.StatusBadRequest, KindBadRequest, "model_required", "model is required")
		return
	}

	start := time.Now()
	var result router.Result
	var err error
	if req.Stream {
		result, err = s.stream(w, r, req)
	} else {
		result, err = s.complete(w, r, req)
	}
	status := http.StatusOK
	if err != nil {
		status = statusOf(err)
	}
	attrs := []any{
		"model", req.Model,
		"status", status,
		"stream", req.Stream,
		"duration_ms", time.Since(start).Milliseconds(),
		"attempts", result.Attempts,
	}
	if result.Model.ID != "" {
		attrs = append(attrs, "connector", result.Model.Connector, "served", result.Model.ID)
	}
	if err != nil {
		attrs = append(attrs, "err", errorKind(err))
	}
	s.logger.Info("chat", attrs...)
}

// authorized checks the optional router Bearer key in constant time.
func (s *Server) authorized(r *http.Request) bool {
	if s.apiKey == "" {
		return true
	}
	const prefix = "Bearer "
	header := r.Header.Get("Authorization")
	if len(header) <= len(prefix) || header[:len(prefix)] != prefix {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(header[len(prefix):]), []byte(s.apiKey)) == 1
}

// stream emits each canonical event as an SSE chunk. A chain that fails
// before the first event still gets a real error status; a mid-stream
// failure degrades to an in-band error event and the client retries.
func (s *Server) stream(w http.ResponseWriter, r *http.Request, req models.ChatRequest) (router.Result, error) {
	encoder := newStreamEncoder(w, req.Model, time.Now().Unix())
	result, err := s.router.Execute(r.Context(), req, encoder.emit)
	if err == nil {
		encoder.finish()
		return result, nil
	}
	if errors.Is(err, catalog.ErrModelNotFound) {
		writeError(w, http.StatusNotFound, KindBadRequest, "model_not_found", err.Error())
		return result, err
	}
	if !encoder.started {
		s.writeConnectorError(w, err)
		return result, err
	}
	var connErr *connector.Error
	if errors.As(err, &connErr) {
		encoder.fail(connErr.Code, connErr.Message)
		return result, err
	}
	encoder.fail("internal", err.Error())
	return result, err
}

// complete folds the event stream into one non-streamed response.
func (s *Server) complete(w http.ResponseWriter, r *http.Request, req models.ChatRequest) (router.Result, error) {
	builder := &completionBuilder{}
	result, err := s.router.Execute(r.Context(), req, func(event models.StreamEvent) error {
		builder.add(event)
		return nil
	})
	if err != nil {
		if errors.Is(err, catalog.ErrModelNotFound) {
			writeError(w, http.StatusNotFound, KindBadRequest, "model_not_found", err.Error())
		} else {
			s.writeConnectorError(w, err)
		}
		return result, err
	}
	writeJSON(w, http.StatusOK, builder.build(req.Model, time.Now().Unix()))
	return result, nil
}

// statusOf mirrors writeConnectorError's mapping for the request log.
func statusOf(err error) int {
	var connErr *connector.Error
	if errors.Is(err, catalog.ErrModelNotFound) {
		return http.StatusNotFound
	}
	if !errors.As(err, &connErr) {
		return http.StatusInternalServerError
	}
	return statusOfConnector(connErr)
}

// statusOfConnector is the single Kind -> status map (01-architecture.md).
// connector_not_ready is a router-side gap: 501, not an upstream verdict.
func statusOfConnector(connErr *connector.Error) int {
	if connErr.Code == "connector_not_ready" {
		return http.StatusNotImplemented
	}
	switch connErr.Kind {
	case connector.KindRateLimit:
		return http.StatusTooManyRequests
	case connector.KindBadRequest, connector.KindUnsupported:
		return http.StatusBadRequest
	case connector.KindUpstreamBad:
		if connErr.Status >= 400 && connErr.Status < 500 {
			return connErr.Status
		}
	}
	return http.StatusBadGateway
}

// errorKind is the log-safe failure tag - Kind only, never the message.
func errorKind(err error) string {
	var connErr *connector.Error
	if errors.As(err, &connErr) {
		return string(connErr.Kind)
	}
	if errors.Is(err, catalog.ErrModelNotFound) {
		return "model_not_found"
	}
	return "internal"
}

// writeConnectorError maps a connector Kind to the status table in
// 01-architecture.md. UpstreamBad passes its own status through.
func (s *Server) writeConnectorError(w http.ResponseWriter, err error) {
	var connErr *connector.Error
	if !errors.As(err, &connErr) {
		writeError(w, http.StatusInternalServerError, KindBadRequest, "internal", err.Error())
		return
	}
	status := statusOfConnector(connErr)
	writeError(w, status, Kind(connErr.Kind), connErr.Code, connErr.Message)
}
