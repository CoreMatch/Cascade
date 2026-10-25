package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"cascade"
	"cascade/internal/app"
	"cascade/internal/config"
)

func main() {
	cfg := config.Load()
	logger := log.New(os.Stdout, "[cascade] ", log.LstdFlags|log.Lmicroseconds)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	application := app.New(cfg, logger, cascade.SDKFS)
	if err := application.Run(ctx); err != nil {
		logger.Fatalf("server exited with error: %v", err)
	}
}
