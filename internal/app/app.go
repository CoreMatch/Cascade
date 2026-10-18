package app

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"

	"rwgin/internal/api"
	"rwgin/internal/config"
	"rwgin/internal/database"
	"rwgin/internal/relay"
)

type App struct {
	cfg        config.Config
	logger     *log.Logger
	db         *database.DB
	gameServer *relay.Server
	httpServer *http.Server
}

func New(cfg config.Config, logger *log.Logger) *App {
	db, err := database.Open(cfg.DBPath, logger)
	if err != nil {
		logger.Fatalf("failed to open database: %v", err)
	}

	gameServer := relay.NewServer(cfg, logger)
	httpServer := &http.Server{
		Addr:    cfg.HTTPListenAddr,
		Handler: api.Router(gameServer.Rooms()),
	}

	return &App{
		cfg:        cfg,
		logger:     logger,
		db:         db,
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
