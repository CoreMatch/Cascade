package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"rwgin"
	"rwgin/internal/app"
	"rwgin/internal/config"
)

func main() {
	cfg := config.Load()
	logger := log.New(os.Stdout, "[rwgin] ", log.LstdFlags|log.Lmicroseconds)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	application := app.New(cfg, logger, rwgin.SDKFS)
	if err := application.Run(ctx); err != nil {
		logger.Fatalf("server exited with error: %v", err)
	}
}
