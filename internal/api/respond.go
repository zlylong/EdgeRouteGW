// Package api is the HTTP layer: routing, middleware, encoding. Handlers call
// into store and (later) reconcile; they contain no business rules.
package api

import (
	"encoding/json"
	"net/http"
)

// ErrorBody is the uniform error envelope: {"error":{"code":..., "message":...}}.
type ErrorBody struct {
	Error ErrorDetail `json:"error"`
}

// ErrorDetail carries a stable machine code and a human message.
type ErrorDetail struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details,omitempty"`
}

// Stable error codes. Add here, never inline strings in handlers.
const (
	CodeNotFound   = "NOT_FOUND"
	CodeBadRequest = "BAD_REQUEST"
	CodeInternal   = "INTERNAL_ERROR"
)

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, ErrorBody{Error: ErrorDetail{Code: code, Message: msg}})
}
