package app

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"cascade/internal/api"
	"cascade/internal/auth"
	"cascade/internal/config"
	"cascade/internal/database"
	"cascade/internal/paths"
	"cascade/internal/plugin"
	"cascade/internal/proxy"
	"cascade/internal/redis"
)

type App struct {
	cfg         config.Config
	logger      *log.Logger
	db          database.Store
	rdb         *redis.Client
	authClient  *auth.Client
	pluginsHub  *plugin.Hub
	proxyServer *proxy.Server
	httpServer  *http.Server
	sdkFS       embed.FS
}

func New(cfg config.Config, logger *log.Logger, sdkFS embed.FS) *App {
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
		// A relative db_path may refer to a database that lives next to the
		// executable when the binary is started from a different working
		// directory; prefer that existing file over creating a fresh one.
		if !filepath.IsAbs(dsn) {
			if resolved, ok := paths.FirstExisting(dsn); ok {
				dsn = resolved
			}
		}
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

	// Out-of-process plugin hub (Plugins.json). A missing/empty manifest
	// yields an idle hub (zero regression); an invalid one is a startup error.
	pluginCfg, pluginPath, err := plugin.LoadConfigFile()
	if err != nil {
		logger.Fatalf("failed to load plugin manifest: %v", err)
	}
	pluginsHub, err := plugin.New(pluginCfg, plugin.ProxyInfo{
		ProxyID:     cfg.ProxyIssuer,
		PublicHost:  cfg.PublicGameHost,
		HostVersion: plugin.HostVersion,
	}, logger)
	if err != nil {
		logger.Fatalf("failed to initialize plugin hub: %v", err)
	}
	logger.Printf("[plugin] manifest %s loaded (%d entries)", pluginPath, len(pluginCfg.Entries))

	proxyServer, err := proxy.NewServer(cfg, logger, db, rdb, authClient, pluginsHub)
	if err != nil {
		logger.Fatalf("failed to initialize proxy: %v", err)
	}
	httpServer := &http.Server{
		Addr:    cfg.HTTPListenAddr,
		Handler: api.ProxyRouter(proxyServer, authClient, db),
	}

	return &App{
		cfg:         cfg,
		logger:      logger,
		db:          db,
		rdb:         rdb,
		authClient:  authClient,
		pluginsHub:  pluginsHub,
		proxyServer: proxyServer,
		httpServer:  httpServer,
		sdkFS:       sdkFS,
	}
}

func (a *App) Run(ctx context.Context) error {
	defer a.pluginsHub.Close()
	errCh := make(chan error, 2)

	// Register with HRPAuth Core and upload SDK (non-blocking)
	if a.authClient != nil {
		go func() {
			time.Sleep(2 * time.Second)
			if err := a.registerService(); err != nil {
				a.logger.Printf("[startup] warning: service registration failed: %v", err)
			}
		}()
		// Keep the presence record alive: the one-shot registration above
		// expires after its TTL unless renewed.
		go presenceLoop(ctx, a.authClient, a.cfg.PresenceTTLSec, presenceInterval(a.cfg.PresenceTTLSec), a.logger)
	}

	// Periodically delete guest bindings idle beyond the store's policy
	// window; independent of the presence loop above.
	go bindingCleanupLoop(ctx, a.db, bindingCleanupInterval, a.logger)

	go func() {
		a.logger.Printf("http listener started on %s", a.cfg.HTTPListenAddr)
		if err := a.httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- fmt.Errorf("http server: %w", err)
		}
	}()

	go func() {
		if err := a.proxyServer.Run(ctx); err != nil {
			errCh <- fmt.Errorf("proxy server: %w", err)
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

// registerService registers this service with HRPAuth Core and uploads the SDK package.
func (a *App) registerService() error {
	// 1. Register presence
	prefix := a.cfg.HRPAuthRelayPrefix
	if prefix == "" {
		prefix = "/rusted-warfare"
	}
	if err := a.authClient.RegisterPresence(a.cfg.PresenceTTLSec); err != nil {
		return fmt.Errorf("register presence: %w", err)
	}
	a.logger.Printf("[startup] presence registered with prefix %s", prefix)

	// 2. Register relay rules
	if err := a.authClient.RegisterRelay(prefix); err != nil {
		return fmt.Errorf("register relay: %w", err)
	}
	a.logger.Printf("[startup] relay rules registered")

	// 3. Refresh internal key (may have changed after registration)
	if err := a.authClient.FetchInternalKey(); err != nil {
		a.logger.Printf("[startup] warning: failed to refresh internal key: %v", err)
	}

	// 4. Upload SDK package
	if err := a.authClient.UploadSDKPackage(a.sdkFS, "rusted-warfare"); err != nil {
		return fmt.Errorf("upload SDK: %w", err)
	}
	a.logger.Printf("[startup] SDK package uploaded")

	return nil
}
