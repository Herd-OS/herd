package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/herd-os/herd/internal/controlplane/reconciler"
	"github.com/herd-os/herd/internal/controlplane/store"
	"github.com/herd-os/herd/internal/service"
)

const defaultAddr = ":8080"

func main() {
	logger := log.New(os.Stderr, "herd-service: ", log.LstdFlags)

	cfg, err := service.LoadConfigFromEnv()
	if err != nil {
		logger.Fatalf("load config: %v", err)
	}
	if err := cfg.Validate(); err != nil {
		logger.Fatalf("validate config: %v", err)
	}

	ctx := context.Background()
	st, err := store.OpenPostgresStore(ctx, cfg.DatabaseURL)
	if err != nil {
		logger.Fatalf("open postgres store: %v", err)
	}
	defer func() {
		if err := st.Close(); err != nil {
			logger.Printf("close store: %v", err)
		}
	}()

	deps := service.Dependencies{
		Logger: logger,
		Store:  st,
	}
	if cfg.ReconcilerEnabled {
		deps.Reconciler = &reconciler.Reconciler{Store: st}
	}
	stopReconciler, started := service.StartReconcilerLoop(ctx, cfg, deps)
	if started {
		defer func() {
			if err := stopReconciler(); err != nil {
				logger.Printf("stop reconciler: %v", err)
			}
		}()
	}

	handler, err := service.NewServer(cfg, deps)
	if err != nil {
		logger.Fatalf("create server: %v", err)
	}

	server := &http.Server{
		Addr:              defaultAddr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
	}

	logger.Printf("listening on %s", defaultAddr)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Fatalf("serve: %v", err)
	}
}
