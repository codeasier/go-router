package syntheticchat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
)

// Local is an owned synthetic HTTP peer, never a configurable provider. Its
// transport dials only the numeric address reserved by its own listener.
type Local struct {
	origin    string
	transport *http.Transport
	server    *http.Server
	listener  net.Listener
}

func StartLocal() (*Local, error) {
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return nil, errors.New("could not start local synthetic peer")
	}
	addr := ln.Addr().String()
	var dialer net.Dialer
	transport := &http.Transport{
		Proxy: nil, DisableKeepAlives: true, DisableCompression: true,
		MaxResponseHeaderBytes: 16 << 10,
		DialContext: func(ctx context.Context, network, target string) (net.Conn, error) {
			if network != "tcp" || target != addr {
				return nil, errors.New("invalid synthetic peer")
			}
			return dialer.DialContext(ctx, "tcp4", addr)
		},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST "+chatPath, localCompletion)
	srv := &http.Server{
		Handler: mux, ReadTimeout: fixtureTimeout, WriteTimeout: fixtureTimeout,
		MaxHeaderBytes: 16 << 10,
	}
	go func() { _ = srv.Serve(ln) }()
	return &Local{origin: "http://" + addr, transport: transport, server: srv, listener: ln}, nil
}

func (l *Local) Close() {
	// Close the owned listener even if Serve hasn't entered its accept loop.
	_ = l.server.Close()
	_ = l.listener.Close()
	l.transport.CloseIdleConnections()
}

func localCompletion(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Model  string `json:"model"`
		Stream bool   `json:"stream"`
	}
	body, ok := readBounded(r.Body, maxRequest)
	if !ok || json.Unmarshal(body, &in) != nil || in.Model == "" {
		http.Error(w, "invalid synthetic request", http.StatusBadRequest)
		return
	}
	// Never echo prompts, credentials or client extension fields. Numbers here
	// are deterministic test values; they are not measured or billable usage.
	model, _ := json.Marshal(in.Model)
	const usage = `{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}`
	if !in.Stream {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"id":"synthetic","object":"chat.completion","model":%s,"choices":[{"index":0,"message":{"role":"assistant","content":"synthetic fixture"},"finish_reason":"stop"}],"usage":%s}`, model, usage)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	fmt.Fprintf(w, "data: "+`{"id":"synthetic","object":"chat.completion.chunk","model":%s,"choices":[{"index":0,"delta":{"role":"assistant","content":"synthetic fixture"},"finish_reason":"stop"}]}`+"\n\n", model)
	fmt.Fprintf(w, "data: "+`{"id":"synthetic","object":"chat.completion.chunk","model":%s,"choices":[],"usage":%s}`+"\n\n", model, usage)
	io.WriteString(w, "data: [DONE]\n\n")
}
