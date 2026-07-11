package main

import (
	"errors"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/herd-os/herd/internal/service"
)

const defaultAddr = ":8080"

func main() {
	logger := log.New(os.Stderr, "herd-service: ", log.LstdFlags)

	cfg, err := service.LoadConfigFromEnv()
	if err != nil {
		logger.Fatalf("load config: %v", err)
	}

	handler, err := service.NewServer(cfg, service.Dependencies{Logger: logger})
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
