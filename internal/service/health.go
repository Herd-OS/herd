package service

import (
	"context"
	"fmt"
	"net/http"
)

type HealthStore interface {
	Health(ctx context.Context) error
}

func registerHealthRoutes(mux *http.ServeMux, cfg Config, deps Dependencies) {
	mux.HandleFunc("GET /healthz", healthzHandler)
	mux.HandleFunc("GET /readyz", readyzHandler(cfg, deps.Store))
}

func healthzHandler(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{
		"status": "ok",
	})
}

func readyzHandler(cfg Config, store HealthStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := cfg.Validate(); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{
				"error": fmt.Sprintf("configuration not ready: %v", err),
			})
			return
		}

		if store == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{
				"error": "storage not ready: dependency is not configured",
			})
			return
		}

		if err := store.Health(r.Context()); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{
				"error": fmt.Sprintf("storage not ready: %v", err),
			})
			return
		}

		writeJSON(w, http.StatusOK, map[string]string{
			"status": "ready",
		})
	}
}
