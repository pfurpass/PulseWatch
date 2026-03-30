package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/pulsewatch/internal/server/alerting"
	"github.com/pulsewatch/internal/server/anomaly"
	"github.com/pulsewatch/internal/server/api"
	"github.com/pulsewatch/internal/server/auth"
	"github.com/pulsewatch/internal/server/config"
	"github.com/pulsewatch/internal/server/ingest"
	"github.com/pulsewatch/internal/server/prom"
	"github.com/pulsewatch/internal/server/storage"
)

func main() {
	var (
		listenAddr  = flag.String("listen",  "",          "HTTP listen address")
		dataDir     = flag.String("data",    "",          "Data directory")
		cfgFile     = flag.String("config",  "",          "Config file")
		agentsFlag  = flag.String("agents",  "",          "Comma-separated agent addresses")
		alertsFile  = flag.String("alerts",  "",          "Alert rules JSON file")
		usersFile   = flag.String("users",   "",          "Users JSON file (enables auth)")
		authSecret  = flag.String("secret",  "changeme",  "HMAC secret for session tokens")
		promTargets = flag.String("prom",    "",          "Comma-separated Prometheus target URLs")
		logLevel    = flag.String("log",     "info",      "Log level")
		anomalyWin  = flag.Int("anomaly-window", 120,    "Anomaly detection window size")
		anomalyThr  = flag.Float64("anomaly-threshold", 3.0, "Z-Score threshold for anomaly")
	)
	flag.Parse()

	// Logger
	lvl := slog.LevelInfo
	switch *logLevel {
	case "debug": lvl = slog.LevelDebug
	case "warn":  lvl = slog.LevelWarn
	case "error": lvl = slog.LevelError
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level:lvl})))

	// Config
	cfg := config.Default()
	config.LoadEnv(&cfg)
	if *cfgFile != "" {
		if err := config.LoadFile(*cfgFile, &cfg); err != nil {
			slog.Error("config load failed", "err", err); os.Exit(1)
		}
	}
	if *listenAddr != "" { cfg.ListenAddr = *listenAddr }
	if *dataDir    != "" { cfg.DataDir    = *dataDir    }
	for _, addr := range splitComma(*agentsFlag) {
		cfg.Agents = append(cfg.Agents, config.AgentConfig{Address:addr})
	}

	// ── Storage ───────────────────────────────────────────────────────────
	store, err := storage.New(cfg.DataDir)
	if err != nil { slog.Error("storage init failed", "err", err); os.Exit(1) }
	store.Start()

	// ── Context ───────────────────────────────────────────────────────────
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// ── LiveCache ─────────────────────────────────────────────────────────
	liveCache := ingest.NewLiveCache()

	// ── Scraper ───────────────────────────────────────────────────────────
	scraper := ingest.New(store, cfg.ScrapeInterval)
	scraper.SetLiveCache(liveCache)

	for _, a := range cfg.Agents {
		scraper.AddTarget(ingest.AgentTarget{Name:a.Name, Address:a.Address})
		go func(addr string) {
			fCtx, fCancel := context.WithTimeout(ctx, 30*time.Second)
			defer fCancel()
			scraper.FetchHistory(fCtx, ingest.AgentTarget{Address:addr})
		}(a.Address)
	}
	go scraper.Run(ctx)

	// ── Alerting ──────────────────────────────────────────────────────────
	alertCfg := alerting.DefaultRulesConfig()
	if *alertsFile != "" {
		if ac, err := alerting.LoadRulesConfig(*alertsFile); err != nil {
			slog.Warn("alert rules load failed, using defaults", "err", err)
		} else {
			alertCfg = ac
			slog.Info("alert rules loaded", "file", *alertsFile, "rules", len(ac.Alerts))
		}
	}
	alertHistory := alerting.NewHistory(cfg.DataDir, 1000)
	alertEngine  := alerting.New(alertCfg, liveCache, alertHistory)
	go alertEngine.Run(ctx, liveCache.Hosts)

	// ── Anomaly Detection ─────────────────────────────────────────────────
	anomalyDetector := anomaly.New(*anomalyWin, *anomalyThr)
	slog.Info("anomaly detection enabled", "window", *anomalyWin, "threshold", *anomalyThr)

	// ── Auth ──────────────────────────────────────────────────────────────
	var authManager *auth.Manager
	if *usersFile != "" {
		authManager = auth.New(*usersFile, *authSecret)
		slog.Info("auth enabled", "users_file", *usersFile)
	} else {
		authManager = auth.New("", "") // disabled
	}

	// ── Prometheus Scraper ────────────────────────────────────────────────
	promScraper := prom.New(store)
	for _, url := range splitComma(*promTargets) {
		promScraper.AddTarget(ctx, prom.Target{URL:url, Interval:15*time.Second})
	}

	// ── API Server ────────────────────────────────────────────────────────
	srv := api.New(store, scraper, api.Options{
		AlertEngine:     alertEngine,
		AnomalyDetector: anomalyDetector,
		AuthManager:     authManager,
		PromScraper:     promScraper,
		LiveCache:       liveCache,
	})
	srv.Start(ctx)

	httpSrv := &http.Server{Addr:cfg.ListenAddr, Handler:srv.Handler()}

	// ── Graceful Shutdown ─────────────────────────────────────────────────
	sigCh := make(chan os.Signal,1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		slog.Info("shutting down...")
		cancel()
		shutCtx, shutCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer shutCancel()
		httpSrv.Shutdown(shutCtx)
		store.Stop()
	}()

	slog.Info("PulseWatch Server ready",
		"listen", cfg.ListenAddr,
		"data", cfg.DataDir,
		"agents", len(cfg.Agents),
		"alert_rules", len(alertCfg.Alerts),
		"auth", authManager.Enabled(),
	)

	fmt.Printf("\n  ┌─ PulseWatch v0.2.0 ─────────────────────────────┐\n")
	fmt.Printf("  │  Dashboard  → http://localhost%s           │\n", cfg.ListenAddr)
	fmt.Printf("  │  API        → http://localhost%s/api/v1/status │\n", cfg.ListenAddr)
	fmt.Printf("  │  Agents     → %d registered                     │\n", len(cfg.Agents))
	fmt.Printf("  │  Alerts     → %d rules active                   │\n", len(alertCfg.Alerts))
	fmt.Printf("  │  Auth       → %v                               │\n", authManager.Enabled())
	fmt.Printf("  └─────────────────────────────────────────────────────┘\n\n")

	if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		slog.Error("server error", "err", err); os.Exit(1)
	}
	slog.Info("server stopped cleanly")
}

func splitComma(s string) []string {
	if s == "" { return nil }
	var r []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" { r = append(r, p) }
	}
	return r
}
