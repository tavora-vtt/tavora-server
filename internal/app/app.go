package app

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/tavora-vtt/tavora-server/internal/transport/httpapi"
)

const (
	defaultBind            = "0.0.0.0:30000"
	defaultShutdownTimeout = 15 * time.Second
	readHeaderTimeout      = 10 * time.Second
)

type Config struct {
	Bind            string
	ShutdownTimeout time.Duration
}

func ConfigFromEnv() Config {
	cfg := Config{
		Bind:            defaultBind,
		ShutdownTimeout: defaultShutdownTimeout,
	}
	if bind := os.Getenv("TAVORA_SERVER_BIND"); bind != "" {
		cfg.Bind = bind
	}
	return cfg
}

type App struct {
	config Config
	log    *slog.Logger
	server *http.Server
}

func New(config Config, log *slog.Logger) (*App, error) {
	return &App{
		config: config,
		log:    log,
		server: &http.Server{
			Addr:              config.Bind,
			Handler:           httpapi.NewRouter(log),
			ReadHeaderTimeout: readHeaderTimeout,
		},
	}, nil
}

func (a *App) Run(ctx context.Context) error {
	failed := make(chan error, 1)

	go func() {
		a.log.Info("listening", "address", a.config.Bind)
		failed <- a.server.ListenAndServe()
	}()

	select {
	case err := <-failed:
		return err
	case <-ctx.Done():
	}

	a.log.Info("shutting down")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), a.config.ShutdownTimeout)
	defer cancel()

	return a.server.Shutdown(shutdownCtx)
}
