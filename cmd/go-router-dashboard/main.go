// go-router-dashboard runs the opt-in, read-only, loopback fleet pilot.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/codeasier/go-router/internal/controlplane"
)

const usage = "Usage: go-router-dashboard -config <private JSON file> [-addr 127.0.0.1:20129]\n       go-router-dashboard preflight -help"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := dispatch(ctx, os.Args[1:], os.LookupEnv, os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

func dispatch(ctx context.Context, args []string, lookup func(string) (string, bool), out, diagnostic io.Writer) int {
	if len(args) > 0 && args[0] == "preflight" {
		return runPreflight(ctx, args[1:], lookup, out, diagnostic)
	}
	return run(ctx, args, lookup, diagnostic)
}

func run(ctx context.Context, args []string, lookup func(string) (string, bool), out io.Writer) int {
	flags := flag.NewFlagSet("go-router-dashboard", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	address := flags.String("addr", "127.0.0.1:20129", "numeric loopback listen address")
	configPath := flags.String("config", "", "private JSON inventory file")
	if err := flags.Parse(args); err != nil {
		fmt.Fprintln(out, usage)
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 || *configPath == "" {
		fmt.Fprintln(out, usage)
		return 2
	}
	addr, err := netip.ParseAddrPort(*address)
	if err != nil || !addr.Addr().IsLoopback() || addr.Addr().Zone() != "" || addr.Port() == 0 {
		fmt.Fprintln(out, "Dashboard address must be a numeric loopback address with a nonzero port.")
		return 2
	}
	addr = netip.AddrPortFrom(addr.Addr().Unmap(), addr.Port())
	nodes, err := loadConfig(*configPath, lookup)
	if err != nil {
		fmt.Fprintln(out, err)
		return 2
	}
	collector, err := controlplane.NewCollector(nodes, 2*time.Second, time.Second)
	if err != nil {
		fmt.Fprintln(out, err) // NewCollector returns only fixed, redacted errors.
		return 2
	}
	listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", addr.String())
	if err != nil {
		fmt.Fprintln(out, "Unable to bind the loopback Dashboard listener.")
		return 1
	}
	view := controlplane.NewDashboard(collector, 6*time.Second)
	server := &http.Server{
		Handler:           localHostOnly(listener.Addr().String(), view),
		ReadTimeout:       5 * time.Second,
		ReadHeaderTimeout: 2 * time.Second,
		WriteTimeout:      5 * time.Second,
		IdleTimeout:       20 * time.Second,
		MaxHeaderBytes:    16 << 10,
		ErrorLog:          log.New(io.Discard, "", 0),
	}
	pollCtx, stopPolling := context.WithCancel(ctx)
	pollDone := make(chan error, 1)
	httpDone := make(chan error, 1)
	go func() { pollDone <- collector.Run(pollCtx) }()
	go func() { httpDone <- server.Serve(listener) }()
	fmt.Fprintln(out, "Local read-only Dashboard is listening.")
	code := 0
	select {
	case <-ctx.Done():
	case err := <-httpDone:
		if !errors.Is(err, http.ErrServerClosed) {
			fmt.Fprintln(out, "Dashboard listener stopped unexpectedly.")
			code = 1
		}
	}
	stopPolling()
	stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := server.Shutdown(stopCtx); err != nil {
		_ = server.Close()
		fmt.Fprintln(out, "Dashboard shutdown timed out.")
		code = 1
	}
	if err := <-pollDone; err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		fmt.Fprintln(out, "Dashboard polling stopped unexpectedly.")
		code = 1
	}
	return code
}

// Reject nonliteral Host headers, including browser attempts to reach the local
// listener under a different origin after a hostname resolves to loopback.
func localHostOnly(host string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != host {
			w.Header().Set("Cache-Control", "no-store")
			http.Error(w, "Use the Dashboard's numeric loopback address.", http.StatusMisdirectedRequest)
			return
		}
		next.ServeHTTP(w, r)
	})
}
