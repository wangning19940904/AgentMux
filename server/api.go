package server

import (
	"encoding/json"
	"net/http"
	"strings"
)

// writeJSON writes v as the JSON response body with the given status code.
func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// writeErr writes the canonical {"error": msg} body with the given status.
func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

// writeOK writes the canonical {"ok": true} success body.
func writeOK(w http.ResponseWriter) {
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// serviceUnavailable writes the canonical 503 body for a missing backend.
func serviceUnavailable(w http.ResponseWriter, what string) {
	writeErr(w, http.StatusServiceUnavailable, what+" unavailable")
}

// decodeJSON decodes the request body into T. On malformed payloads it writes
// a 400 response and reports ok=false; handlers should just return.
func decodeJSON[T any](w http.ResponseWriter, r *http.Request) (T, bool) {
	var v T
	if err := json.NewDecoder(r.Body).Decode(&v); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return v, false
	}
	return v, true
}

// decodeJSONInto decodes the request body into v (a pointer), for handlers
// that declare anonymous request structs. On malformed payloads it writes a
// 400 response and reports false; handlers should just return.
func decodeJSONInto(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return false
	}
	return true
}

// requireQuery returns the named query parameter. When missing or blank it
// writes a 400 response and reports ok=false; handlers should just return.
func requireQuery(w http.ResponseWriter, r *http.Request, name string) (string, bool) {
	value := strings.TrimSpace(r.URL.Query().Get(name))
	if value == "" {
		writeErr(w, http.StatusBadRequest, "missing "+name)
		return "", false
	}
	return value, true
}
