package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/pulsewatch/internal/agent"
)

func main() {
	var (
		listenAddr = flag.String("listen", ":19998", "HTTP/WebSocket listen address")
		interval   = flag.Duration("interval", time.Second, "Collection interval")
		logLevel   = flag.String("log", "info", "Log level: debug, info, warn, error")
	)
	flag.Parse()

	// Logger konfigurieren
	level := slog.LevelInfo
	switch *logLevel {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: level,
	}))
	slog.SetDefault(logger)

	// Agent erstellen
	cfg := agent.Config{
		Interval:   *interval,
		ListenAddr: *listenAddr,
	}

	a, err := agent.New(cfg)
	if err != nil {
		slog.Error("failed to create agent", "err", err)
		os.Exit(1)
	}

	// Graceful Shutdown via SIGTERM / SIGINT
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		sig := <-sigCh
		slog.Info("received signal, shutting down", "signal", sig)
		cancel()
	}()

	// Agent starten (blockiert)
	if err := a.Run(ctx, *listenAddr); err != nil {
		slog.Error("agent error", "err", err)
		os.Exit(1)
	}

	slog.Info("Agent stopped cleanly")
}
