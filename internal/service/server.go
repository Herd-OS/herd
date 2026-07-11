package service

import (
	"context"
	"log"
	"net/http"
	"os"

	cpgithub "github.com/herd-os/herd/internal/controlplane/github"
)

type Dependencies struct {
	Logger                  *log.Logger
	Store                   Store
	RegisterRepositoryRoute http.Handler
}

type Store interface {
	cpgithub.Store
	Health(ctx context.Context) error
}

func NewServer(cfg Config, deps Dependencies) (http.Handler, error) {
	if deps.Logger == nil {
		deps.Logger = log.New(os.Stderr, "herd-service: ", log.LstdFlags)
	}

	mux := http.NewServeMux()
	registerHealthRoutes(mux, cfg, deps)
	registerAPIRoutes(mux, cfg, deps)

	return mux, nil
}
