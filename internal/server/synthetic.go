package server

import (
	"errors"
	"net/http"
	"net/netip"
	"time"
)

// NewLocalSynthetic is a separate, explicitly enabled listener mode. It
// cannot inherit the ordinary listener's public default or accept browser
// requests from an unrelated origin. It never mounts management endpoints.
func NewLocalSynthetic(addr string, routes http.Handler) (*Server, error) {
	return newLocalInference(addr, routes, 15*time.Second)
}

// NewLocalProvider mounts a loopback-only provider pilot, with a write
// deadline longer than its bounded upstream attempt.
func NewLocalProvider(addr string, routes http.Handler) (*Server, error) {
	return newLocalInference(addr, routes, 40*time.Second)
}

func newLocalInference(addr string, routes http.Handler, writeTimeout time.Duration) (*Server, error) {
	ap, err := netip.ParseAddrPort(addr)
	if err != nil || !ap.Addr().IsLoopback() || ap.Addr().Zone() != "" || ap.Port() == 0 || routes == nil {
		return nil, errors.New("local inference requires a numeric loopback inference address and nonzero port")
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", handleHealthz)
	mux.Handle("/", routes)
	guard := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != addr || len(r.Header.Values("Origin")) != 0 {
			http.Error(w, "invalid local request", http.StatusForbidden)
			return
		}
		mux.ServeHTTP(w, r)
	})
	return &Server{httpServer: &http.Server{
		Addr: addr, Handler: guard,
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second,
		WriteTimeout: writeTimeout, IdleTimeout: 30 * time.Second,
		MaxHeaderBytes: 16 << 10,
	}}, nil
}
