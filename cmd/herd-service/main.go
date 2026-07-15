package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

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
	st, err := openServiceStore(ctx, cfg)
	if err != nil {
		logger.Fatalf("open postgres store: %v", err)
	}
	if st != nil {
		defer func() {
			if err := st.Close(); err != nil {
				logger.Printf("close store: %v", err)
			}
		}()
	}

	deps, err := buildServiceDependencies(cfg, st, logger)
	if err != nil {
		logger.Fatalf("build service dependencies: %v", err)
	}
	handler, err := service.NewServer(cfg, deps)
	if err != nil {
		logger.Fatalf("create server: %v", err)
	}
	stopReconciler, started := service.StartReconcilerLoop(ctx, cfg, deps)
	if started {
		defer func() {
			if err := stopReconciler(); err != nil {
				logger.Printf("stop reconciler: %v", err)
			}
		}()
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

func openServiceStore(ctx context.Context, cfg service.Config) (*store.PostgresStore, error) {
	if strings.TrimSpace(cfg.DatabaseURL) == "" {
		if cfg.Env == "production" || cfg.Env == "staging" {
			return nil, fmt.Errorf("HERD_DATABASE_URL is required")
		}
		return nil, nil
	}
	return store.OpenPostgresStore(ctx, cfg.DatabaseURL)
}
