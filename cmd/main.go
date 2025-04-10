package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/thunderbottom/damon/internal/config"
	"github.com/thunderbottom/damon/internal/core"

	_ "github.com/thunderbottom/damon/provider/dns"
	_ "github.com/thunderbottom/damon/provider/nomad"
)

var (
	// Build version, injected at build time
	buildString = "unknown"
)

func main() {
	// Create root context with cancellation
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Set up signal handling
	signalCh := make(chan os.Signal, 1)
	signal.Notify(signalCh, syscall.SIGINT, syscall.SIGTERM)
	go handleSignals(signalCh, cancel)

	// Initialize a simple logger for loading configuration
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

	// Load configuration
	cfg, err := config.Load()
	if err != nil {
		logger.Error("failed to load configuration", "error", err)
		os.Exit(1)
	}

	// Update logger with configured log level
	logger = cfg.NewLogger()
	logger.Info("starting damon", "version", buildString)

	// Initialize core
	app, err := core.New(ctx, cfg, logger)
	if err != nil {
		logger.Error("failed to initialize application", "error", err)
		os.Exit(1)
	}
	defer app.Close()

	// Run application
	if err := app.Run(ctx); err != nil && err != context.Canceled {
		logger.Error("application terminated with error", "error", err)
		os.Exit(1)
	}

	logger.Info("application shutdown complete")
}

// handleSignals processes OS signals for graceful shutdown
func handleSignals(signalCh <-chan os.Signal, cancel context.CancelFunc) {
	sig := <-signalCh
	fmt.Printf("\nReceived signal %s, initiating shutdown...\n", sig)
	cancel()
}
