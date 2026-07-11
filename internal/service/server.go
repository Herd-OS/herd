package service

import (
	"log"
	"net/http"
	"os"
)

type Dependencies struct {
	Logger *log.Logger
	Store  HealthStore
}

func NewServer(cfg Config, deps Dependencies) (http.Handler, error) {
	if deps.Logger == nil {
		deps.Logger = log.New(os.Stderr, "herd-service: ", log.LstdFlags)
	}

	mux := http.NewServeMux()
	registerHealthRoutes(mux, cfg, deps)
	registerAPIRoutes(mux)

	return mux, nil
}
