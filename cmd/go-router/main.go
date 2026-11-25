package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/codeasier/go-router/internal/server"
	"github.com/codeasier/go-router/internal/version"
)

func main() {
	addr := flag.String("addr", ":20128", "inference listen address")
	adminAddr := flag.String("admin-addr", "", "optional read-only management listener (numeric loopback address:port; requires -node-id and GO_ROUTER_MANAGEMENT_KEY)")
	nodeID := flag.String("node-id", "", "stable, non-secret node ID for the management listener")
	configPath := flag.String("config", "", "optional local node metadata JSON; reload with SIGHUP on Unix")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Printf("go-router %s (commit %s, built %s)\n", version.Version, version.Commit, version.Date)
		return
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	var key string
	if *adminAddr != "" {
		key = os.Getenv("GO_ROUTER_MANAGEMENT_KEY")
	}
	var config *localConfiguration
	var reloads <-chan os.Signal
	if *configPath != "" {
		config = newLocalConfiguration(*configPath, key)
		var stopReload func()
		reloads, stopReload = reloadNotifications()
		defer stopReload()
		if _, err := config.load(context.Background()); err != nil {
			logger.Error("invalid node configuration")
			os.Exit(1)
		}
	}

	servers := []*server.Server{server.New(*addr)}
	if *adminAddr != "" {
		admin, err := server.NewManagementWithRevision(*adminAddr, *nodeID, key, config.revision)
		if err != nil {
			logger.Error("invalid management configuration", "err", err)
			os.Exit(1)
		}
		servers = append(servers, admin)
	} else if *nodeID != "" {
		logger.Error("node ID requires a management listener")
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	// Run file reloads off the HTTP/shutdown path. A slow local read must
	// not delay shutting down the listeners, and cancellation prevents a
	// candidate returned by an interrupted read from being published.
	reloadCtx, stopReloading := context.WithCancel(ctx)
	defer stopReloading()
	if reloads != nil {
		go config.watch(reloadCtx, reloads, logger)
	}
	logger.Info("starting go-router", "addr", *addr, "version", version.Version)
	if err := serve(ctx, servers, func() {
		stopReloading()
		config.fence()
	}); err != nil {
		logger.Error("server failed", "err", err)
		os.Exit(1)
	}
	logger.Info("stopped")
}

func serve(ctx context.Context, servers []*server.Server, beforeDrain func()) error {
	errCh := make(chan error, len(servers))
	for _, srv := range servers {
		go func() { errCh <- srv.ListenAndServe() }()
	}

	var failure error
	select {
	case <-ctx.Done():
	case err := <-errCh:
		if !errors.Is(err, http.ErrServerClosed) {
			failure = err
		}
	}

	beforeDrain()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, srv := range servers {
		failure = errors.Join(failure, srv.Shutdown(shutdownCtx))
	}
	return failure
}
