package app

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/tavora-vtt/tavora-server/internal/core/access"
	"github.com/tavora-vtt/tavora-server/internal/core/auth"
	"github.com/tavora-vtt/tavora-server/internal/core/blob"
	"github.com/tavora-vtt/tavora-server/internal/core/perm"
	"github.com/tavora-vtt/tavora-server/internal/storage"
	"github.com/tavora-vtt/tavora-server/internal/storage/postgres"
	"github.com/tavora-vtt/tavora-server/internal/storage/sqlite"
	"github.com/tavora-vtt/tavora-server/internal/transport/httpapi"
	"github.com/tavora-vtt/tavora-server/internal/transport/ws"
)

const (
	defaultBind            = "0.0.0.0:30000"
	defaultBlobRoot        = "data/blobs"
	defaultWorldQuota      = 4 << 30
	defaultStorageDriver   = "sqlite"
	defaultStorageDSN      = "data/tavora.db"
	defaultShutdownTimeout = 15 * time.Second
	readHeaderTimeout      = 10 * time.Second
	migrationTimeout       = 60 * time.Second
)

type Config struct {
	Bind            string
	BlobRoot        string
	AssetBaseURL    string
	WorldQuota      int64
	StorageDriver   string
	StorageDSN      string
	JSONProtocol    bool
	SecureCookies   bool
	ShutdownTimeout time.Duration
}

func ConfigFromEnv() Config {
	config := Config{
		Bind:            defaultBind,
		BlobRoot:        defaultBlobRoot,
		WorldQuota:      defaultWorldQuota,
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
	if value := os.Getenv("TAVORA_BLOB_ROOT"); value != "" {
		config.BlobRoot = value
	}
	if value := os.Getenv("TAVORA_ASSET_BASE_URL"); value != "" {
		config.AssetBaseURL = value
	}
	if value := os.Getenv("TAVORA_WORLD_QUOTA_BYTES"); value != "" {
		if parsed, err := strconv.ParseInt(value, 10, 64); err == nil {
			config.WorldQuota = parsed
		}
	}
	if os.Getenv("TAVORA_PROTOCOL_JSON") == "1" {
		config.JSONProtocol = true
	}
	if os.Getenv("TAVORA_SECURE_COOKIES") == "1" {
		config.SecureCookies = true
	}
	return config
}

type App struct {
	config  Config
	log     *slog.Logger
	store   storage.Store
	gateway *ws.Gateway
	server  *http.Server
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

	resolver := access.NewResolver(store, perm.OpenPolicy{})

	router := ws.NewRouter()
	ws.RegisterCoreIntents(router)
	ws.RegisterChatIntents(router)
	ws.RegisterCombatIntents(router)
	ws.RegisterDoorIntents(router)

	tickets := ws.NewTicketStore(ws.DefaultTicketTTL)

	gateway := ws.NewGateway(ws.Deps{
		Store:    store,
		Registry: ws.NewRegistry(log),
		Tickets:  tickets,
		Router:   router,
		Access:   resolver,
		Log:      log,
	})

	gateway.AllowJSONFormat(config.JSONProtocol)

	if config.JSONProtocol {
		log.Info("readable json protocol available at /ws?format=json")
	}

	blobs, err := blob.NewDisk(config.BlobRoot)
	if err != nil {
		_ = store.Close()
		return nil, err
	}
	log.Info("asset storage ready", "root", config.BlobRoot)

	authService := auth.NewService(store, auth.Options{})

	needsSetup, err := authService.NeedsSetup(ctx)
	if err != nil {
		_ = store.Close()
		return nil, fmt.Errorf("check setup state: %w", err)
	}
	if needsSetup {
		log.Warn("no accounts exist yet, create the first administrator at POST /api/setup")
	}

	handler := httpapi.NewRouter(httpapi.Deps{
		Log:     log,
		Ready:   store.Ping,
		Backend: store.Backend(),
		Auth: httpapi.AuthDeps{
			Service:       authService,
			Store:         store,
			Tickets:       tickets,
			Access:        resolver,
			SecureCookies: config.SecureCookies,
			Assets: httpapi.AssetDeps{
				Blobs:        blobs,
				WorldQuota:   config.WorldQuota,
				AssetBaseURL: config.AssetBaseURL,
			},
		},
		WebSocket: gateway.WebSocketHandler(),
	})

	return &App{
		config:  config,
		log:     log,
		store:   store,
		gateway: gateway,
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
		a.gateway.Close()
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
