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

	"github.com/codeasier/go-router/internal/attemptlog"
	"github.com/codeasier/go-router/internal/server"
	"github.com/codeasier/go-router/internal/version"
)

func main() {
	addr := flag.String("addr", ":20128", "inference listen address")
	adminAddr := flag.String("admin-addr", "", "optional read-only management listener (numeric loopback address:port; requires -node-id and GO_ROUTER_MANAGEMENT_KEY)")
	nodeID := flag.String("node-id", "", "stable, non-secret node ID for the management listener")
	configPath := flag.String("config", "", "optional local node metadata JSON; reload with SIGHUP on Unix")
	syntheticDir := flag.String("synthetic-journal", "", "opt in to local synthetic Chat using an initialized private journal directory; requires -config and numeric loopback -addr")
	genericBase := flag.String("generic-chat-base-url", "", "enable one operator-configured HTTPS Chat upstream; numeric loopback -addr only")
	genericProvider := flag.String("generic-chat-provider", "", "generic provider namespace")
	genericModels := flag.String("generic-chat-models", "", "comma-separated upstream model IDs")
	genericJournal := flag.String("generic-chat-journal", "", "existing private dispatch journal for the Chat pilot")
	volceModels := flag.String("volce-chat-models", "", "also enable VolceAPI native Chat with these comma-separated upstream models")
	xaiModels := flag.String("xai-chat-models", "", "enable xAI API-key native Chat with these comma-separated models")
	xiaomiModels := flag.String("xiaomi-chat-models", "", "enable Xiaomi Token Plan native Chat with these comma-separated models")
	xiaomiRegion := flag.String("xiaomi-chat-region", "", "Xiaomi Token Plan region: sgp (default), cn or ams")
	initJournal := flag.String("init-synthetic-journal", "", "exclusively initialize a journal in an existing absolute private directory and exit")
	initChatJournal := flag.String("init-generic-chat-journal", "", "exclusively initialize a generic Chat journal and exit")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if *initJournal != "" || *initChatJournal != "" {
		dir := *initJournal
		if *initChatJournal != "" {
			dir = *initChatJournal
		}
		if flag.NFlag() != 1 || flag.NArg() != 0 {
			logger.Error("journal initialization is a standalone operation")
			os.Exit(1)
		}
		j, err := attemptlog.Create(dir)
		if err != nil || j.Close() != nil {
			logger.Error("could not initialize dispatch journal")
			os.Exit(1)
		}
		return
	}
	if *showVersion {
		fmt.Printf("go-router %s (commit %s, built %s)\n", version.Version, version.Commit, version.Date)
		return
	}

	var key string
	if *adminAddr != "" {
		key = os.Getenv("GO_ROUTER_MANAGEMENT_KEY")
	}
	var config *localConfiguration
	if *configPath != "" {
		config = newLocalConfiguration(*configPath, key)
	}
	inference := server.New(*addr)
	if *genericBase != "" || *genericProvider != "" || *genericModels != "" || *genericJournal != "" || *volceModels != "" || *xaiModels != "" || *xiaomiModels != "" || *xiaomiRegion != "" {
		if *syntheticDir != "" || *configPath != "" || *adminAddr != "" || *nodeID != "" {
			logger.Error("provider Chat cannot be combined with synthetic, node configuration or management modes")
			os.Exit(1)
		}
		pilot, cleanup, err := prepareProviderChat(*addr, *genericProvider, *genericModels, *genericBase, *volceModels, *xaiModels, *xiaomiModels, *xiaomiRegion, *genericJournal)
		if err != nil {
			logger.Error("invalid provider Chat configuration", "err", err)
			os.Exit(1)
		}
		inference = pilot
		defer cleanup()
	}
	if *syntheticDir != "" {
		var cleanup func()
		var err error
		inference, cleanup, err = prepareSynthetic(*addr, *syntheticDir, config, logger)
		if err != nil {
			logger.Error("invalid synthetic configuration", "err", err)
			os.Exit(1)
		}
		defer cleanup()
	}
	var reloads <-chan os.Signal
	if config != nil {
		var stopReload func()
		reloads, stopReload = reloadNotifications()
		defer stopReload()
		if _, err := config.load(context.Background()); err != nil {
			logger.Error("invalid node configuration")
			os.Exit(1)
		}
	}

	servers := []*server.Server{inference}
	if *adminAddr != "" {
		admin, err := server.NewManagementWithIdentity(*adminAddr, *nodeID, key, config.identity)
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
