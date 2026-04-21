package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	incusclient "github.com/lxc/incus/v6/client"

	"github.com/bmu-ailab/incus-dns-sync/internal/config"
	"github.com/bmu-ailab/incus-dns-sync/internal/dns"
	"github.com/bmu-ailab/incus-dns-sync/internal/reconciler"
)

const version = "1.0.0"

func main() {
	var (
		cfgPath     = flag.String("config", "/etc/incus-dns-sync/incus-dns-sync.yaml", "Config file path")
		showVersion = flag.Bool("version", false, "Version info")
	)
	flag.Parse()

	if *showVersion {
		fmt.Printf("incus-dns-sync v%s\n", version)
		os.Exit(0)
	}

	// Load Config
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "FATAL config load error: %v\n", err)
		os.Exit(1)
	}

	// Logger
	logger := newLogger(cfg.Log.Level)

	logger.Info("incus-dns-sync starting",
		"version", version,
		"config", *cfgPath,
		"zone", cfg.DNS.Zone,
		"profile", cfg.Incus.DNSProfile,
	)

	// Incus connection
	socketPath := cfg.Incus.SocketPath
	if socketPath == "" {
		socketPath = "" // ConnectIncusUnix finds default socket
	}
	incusConn, err := incusclient.ConnectIncusUnix(socketPath, nil)
	if err != nil {
		logger.Error("Incus connection error",
			"socket", socketPath,
			"err", err,
		)
		os.Exit(1)
	}
	logger.Info("Incus connection established")

	// DNS client
	dnsClient := dns.New(
		cfg.Technitium.APIBase,
		cfg.Technitium.Token,
		cfg.DNS.Zone,
		cfg.DNS.TTL,
		cfg.Technitium.Timeout,
	)

	// Technitium access check
	pingCtx, cancel := context.WithTimeout(context.Background(), cfg.Technitium.Timeout)
	if err := dnsClient.Ping(pingCtx); err != nil {
		logger.Warn("Technitium API unreachable — service continues", "err", err)
	} else {
		logger.Info("Technitium connection established")
	}
	cancel()

	// Reconciler
	rec := reconciler.New(cfg, incusConn, dnsClient, logger)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	if err := rec.Run(ctx); err != nil {
		logger.Error("reconciler error", "err", err)
		os.Exit(1)
	}

	logger.Info("incus-dns-sync stopped")
}

func newLogger(level string) *slog.Logger {
	var lvl slog.Level
	switch level {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}

	handler := slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: lvl,
		// Show timestamp in standard format
		ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
			return a
		},
	})
	return slog.New(handler)
}
