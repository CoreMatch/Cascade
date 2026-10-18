package app

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"

	"rwgin/internal/api"
	"rwgin/internal/auth"
	"rwgin/internal/config"
	"rwgin/internal/database"
	"rwgin/internal/redis"
	"rwgin/internal/relay"
)

type App struct {
	cfg        config.Config
	logger     *log.Logger
	db         database.Store
	rdb        *redis.Client
	authClient *auth.Client
	gameServer *relay.Server
	httpServer *http.Server
}

func New(cfg config.Config, logger *log.Logger) *App {
	// Build DSN based on DBType
	dsn := cfg.DBPath
	if cfg.DBType == "mysql" {
		if cfg.MySQLDSN == "" {
			logger.Fatal("MySQL DSN is required when db_type=mysql (set mysql_dsn in config.yaml or RWGIN_MYSQL_DSN env)")
		}
		dsn = cfg.MySQLDSN
	}

	// Ensure SQLite directory exists
	if cfg.DBType == "sqlite" || cfg.DBType == "" {
		if err := os.MkdirAll(filepath.Dir(dsn), 0755); err != nil {
			logger.Fatalf("failed to create db directory: %v", err)
		}
	}

	dbCfg := database.DBConfig{
		Type: cfg.DBType,
		DSN:  dsn,
	}

	db, err := database.NewGORMStore(dbCfg, logger)
	if err != nil {
		logger.Fatalf("failed to open database: %v", err)
	}

	var rdb *redis.Client
	if cfg.RedisAddr != "" {
		rdb, err = redis.New(cfg.RedisAddr, cfg.RedisPassword, cfg.RedisDB, cfg.RedisPrefix, logger)
		if err != nil {
			logger.Fatalf("failed to connect to redis: %v", err)
		}
	}

	// Create auth client if HRPAuth is configured
	var authClient *auth.Client
	if cfg.HRPAuthURL != "" {
		authClient = auth.New(cfg.HRPAuthURL, cfg.HRPAuthClientID, cfg.HRPAuthClientSecret, cfg.HRPAuthServiceName)
		// Fetch internal key from Core for inbound request validation
		if err := authClient.FetchInternalKey(); err != nil {
			logger.Printf("[startup] warning: failed to fetch internal key: %v", err)
		} else {
			logger.Printf("[startup] internal key fetched successfully")
		}
	}

	gameServer := relay.NewServer(cfg, logger, db, rdb, authClient)
	httpServer := &http.Server{
		Addr:    cfg.HTTPListenAddr,
		Handler: api.Router(gameServer.Rooms(), authClient, db),
	}

	return &App{
		cfg:        cfg,
		logger:     logger,
		db:         db,
		rdb:        rdb,
		authClient: authClient,
		gameServer: gameServer,
		httpServer: httpServer,
	}
}

func (a *App) Run(ctx context.Context) error {
	errCh := make(chan error, 2)

	go func() {
		a.logger.Printf("http listener started on %s", a.cfg.HTTPListenAddr)
		if err := a.httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- fmt.Errorf("http server: %w", err)
		}
	}()

	go func() {
		if err := a.gameServer.ListenAndServe(ctx); err != nil {
			errCh <- fmt.Errorf("game server: %w", err)
		}
	}()

	select {
	case <-ctx.Done():
		return a.httpServer.Shutdown(context.Background())
	case err := <-errCh:
		_ = a.httpServer.Shutdown(context.Background())
		return err
	}
}
