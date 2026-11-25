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
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Printf("go-router %s (commit %s, built %s)\n", version.Version, version.Commit, version.Date)
		return
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	servers := []*server.Server{server.New(*addr)}
	if *adminAddr != "" {
		admin, err := server.NewManagement(*adminAddr, *nodeID, os.Getenv("GO_ROUTER_MANAGEMENT_KEY"))
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
	logger.Info("starting go-router", "addr", *addr, "version", version.Version)
	if err := serve(ctx, servers); err != nil {
		logger.Error("server failed", "err", err)
		os.Exit(1)
	}
	logger.Info("stopped")
}

func serve(ctx context.Context, servers []*server.Server) error {
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

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, srv := range servers {
		failure = errors.Join(failure, srv.Shutdown(shutdownCtx))
	}
	return failure
}
