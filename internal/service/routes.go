package service

import (
	"encoding/json"
	"net/http"

	cpgithub "github.com/herd-os/herd/internal/controlplane/github"
)

func registerAPIRoutes(mux *http.ServeMux, cfg Config, deps Dependencies) {
	mux.Handle("POST /webhooks/github", cpgithub.NewHandler(cfg.WebhookSecret, deps.Store, deps.Logger))
	mux.HandleFunc("POST /api/v1/github/repositories/register", notImplementedHandler)
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
