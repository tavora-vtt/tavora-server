package app

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/tavora-vtt/tavora-server/internal/storage"
	"github.com/tavora-vtt/tavora-server/internal/storage/postgres"
	"github.com/tavora-vtt/tavora-server/internal/storage/sqlite"
	"github.com/tavora-vtt/tavora-server/internal/transport/httpapi"
)

const (
	defaultBind            = "0.0.0.0:30000"
	defaultStorageDriver   = "sqlite"
	defaultStorageDSN      = "data/tavora.db"
	defaultShutdownTimeout = 15 * time.Second
	readHeaderTimeout      = 10 * time.Second
	migrationTimeout       = 60 * time.Second
)

type Config struct {
	Bind            string
	StorageDriver   string
	StorageDSN      string
	ShutdownTimeout time.Duration
}

func ConfigFromEnv() Config {
	config := Config{
		Bind:            defaultBind,
		StorageDriver:   defaultStorageDriver,
		StorageDSN:      defaultStorageDSN,
		ShutdownTimeout: defaultShutdownTimeout,
	}
	if value := os.Getenv("TAVORA_SERVER_BIND"); value != "" {
		config.Bind = value
	}
	if value := os.Getenv("TAVORA_STORAGE_DRIVER"); value != "" {
		config.StorageDriver = value
	}
	if value := os.Getenv("TAVORA_STORAGE_DSN"); value != "" {
		config.StorageDSN = value
	}
	return config
}

type App struct {
	config Config
	log    *slog.Logger
	store  storage.Store
	server *http.Server
}

func New(config Config, log *slog.Logger) (*App, error) {
	store, err := openStore(config)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), migrationTimeout)
	defer cancel()

	if err := store.Migrate(ctx); err != nil {
		_ = store.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	log.Info("storage ready", "backend", store.Backend())

	handler := httpapi.NewRouter(httpapi.Deps{
		Log:     log,
		Ready:   store.Ping,
		Backend: store.Backend(),
	})

	return &App{
		config: config,
		log:    log,
		store:  store,
		server: &http.Server{
			Addr:              config.Bind,
			Handler:           handler,
			ReadHeaderTimeout: readHeaderTimeout,
		},
	}, nil
}

func openStore(config Config) (storage.Store, error) {
	switch config.StorageDriver {
	case "sqlite":
		if directory := filepath.Dir(config.StorageDSN); directory != "." && directory != "" {
			if err := os.MkdirAll(directory, 0o750); err != nil {
				return nil, fmt.Errorf("create data directory: %w", err)
			}
		}
		return sqlite.Open(config.StorageDSN)
	case "postgres":
		return postgres.Open(config.StorageDSN)
	default:
		return nil, fmt.Errorf("unknown storage driver %q", config.StorageDriver)
	}
}

func (a *App) Store() storage.Store {
	return a.store
}

func (a *App) Run(ctx context.Context) error {
	defer func() {
		if err := a.store.Close(); err != nil {
			a.log.Error("closing storage", "error", err)
		}
	}()

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
