package service

import (
	"encoding/json"
	"net/http"
)

func registerAPIRoutes(mux *http.ServeMux, deps Dependencies) {
	mux.HandleFunc("POST /webhooks/github", notImplementedHandler)
	if deps.RegistrationHandler != nil {
		mux.Handle("POST /api/v1/github/repositories/register", deps.RegistrationHandler)
	} else {
		mux.HandleFunc("POST /api/v1/github/repositories/register", notImplementedHandler)
	}
	mux.HandleFunc("POST /api/v1/runners/registration-token", notImplementedHandler)
	mux.HandleFunc("POST /api/v1/jobs/{job_id}/results", notImplementedHandler)
}

func notImplementedHandler(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusNotImplemented, map[string]string{
		"error": "not implemented",
	})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
