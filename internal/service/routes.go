package service

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/herd-os/herd/internal/appauth"
	cpgithub "github.com/herd-os/herd/internal/controlplane/github"
)

func registerAPIRoutes(mux *http.ServeMux, cfg Config, deps Dependencies) {
	mux.Handle("POST /webhooks/github", cpgithub.NewHandler(cfg.WebhookSecret, deps.Store, deps.Logger))
	registerHandler := deps.RegisterRepositoryRoute
	if registerHandler == nil && deps.Store != nil {
		_, ok := deps.Store.(cpgithub.RegistrationStore)
		if !ok {
			registerHandler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				writeJSON(w, http.StatusInternalServerError, map[string]string{
					"error": "repository registration storage is not configured",
				})
			})
		}
	}
	if registerHandler == nil && deps.Store != nil {
		registrationStore := deps.Store.(cpgithub.RegistrationStore)
		var err error
		registerHandler, err = cpgithub.NewDefaultRegisterHandler(registrationStore, appauth.AppConfig{
			AppID:         cfg.GitHubAppID,
			PrivateKeyPEM: []byte(cfg.GitHubAppPrivateKey),
		}, cfg.AppLogin, cfg.PublicURL)
		if err != nil {
			registerHandler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				writeJSON(w, http.StatusInternalServerError, map[string]string{
					"error": fmt.Sprintf("repository registration GitHub App auth is not configured: %s", err),
				})
			})
		}
	}
	if registerHandler == nil {
		registerHandler = http.HandlerFunc(notImplementedHandler)
	}
	mux.Handle("POST /api/v1/github/repositories/register", registerHandler)
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
