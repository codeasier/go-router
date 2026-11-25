// Package testupstream provides a scripted, loopback-only HTTP upstream for
// gateway tests. It never contacts a provider or records request contents.
package testupstream

import (
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
)

// Mode selects the entire response script; it is not inferred from the
// incoming JSON. Request encoding and provider-specific parity need other tests.
type Mode int

const (
	Completion Mode = iota
	Stream
	RateLimited
	Unavailable
	InterruptedStream
	WaitForCancel
)

const (
	completion = `{"id":"chatcmpl-test","object":"chat.completion","created":1,"model":"test-model","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}}`
	firstChunk = `{"id":"chatcmpl-test","object":"chat.completion.chunk","created":1,"model":"test-model","choices":[{"index":0,"delta":{"role":"assistant","content":"hello"},"finish_reason":null}]}`
	lastChunk  = `{"id":"chatcmpl-test","object":"chat.completion.chunk","created":1,"model":"test-model","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`
)

// Upstream scripts POST /v1/chat/completions. Start creates one fixture
// per test; cleanup disconnects any clients still waiting for a stream.
type Upstream struct {
	URL    string
	Client *http.Client

	cancelled chan struct{}
	cancel    sync.Once
	requests  atomic.Int64
	open      atomic.Int64
}

// Start creates a fresh upstream and an instrumented HTTP client. Use Client
// for the gateway's upstream transport when testing response-body ownership.
func Start(t testing.TB, mode Mode) *Upstream {
	t.Helper()
	if mode < Completion || mode > WaitForCancel {
		t.Fatalf("testupstream: unknown response mode %d", mode)
	}
	u := &Upstream{cancelled: make(chan struct{})}
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		u.requests.Add(1)
		// Consume, don't store, the request. net/http can then observe a
		// disconnect while the WaitForCancel response is still open.
		if _, err := io.Copy(io.Discard, http.MaxBytesReader(w, r.Body, 1<<20)); err != nil {
			http.Error(w, "invalid test request body", http.StatusBadRequest)
			return
		}
		switch mode {
		case Completion, RateLimited, Unavailable:
			w.Header().Set("Content-Type", "application/json")
			switch mode {
			case Completion:
				io.WriteString(w, completion)
			case RateLimited:
				w.WriteHeader(http.StatusTooManyRequests)
				io.WriteString(w, `{"error":{"type":"rate_limit_error","code":"rate_limit_exceeded","message":"synthetic rate limit"}}`)
			case Unavailable:
				w.WriteHeader(http.StatusServiceUnavailable)
				io.WriteString(w, `{"error":{"type":"server_error","code":"unavailable","message":"synthetic unavailability"}}`)
			}
		case Stream, InterruptedStream, WaitForCancel:
			w.Header().Set("Content-Type", "text/event-stream")
			if _, err := io.WriteString(w, "data: "+firstChunk+"\n\n"); err != nil {
				return
			}
			if err := http.NewResponseController(w).Flush(); err != nil {
				return
			}
			switch mode {
			case Stream:
				io.WriteString(w, "data: "+lastChunk+"\n\ndata: [DONE]\n\n")
			case InterruptedStream:
				// Abort the real HTTP response after flushing a complete
				// first frame: not a successful EOF and not a [DONE].
				panic(http.ErrAbortHandler)
			case WaitForCancel:
				<-r.Context().Done()
				u.cancel.Do(func() { close(u.cancelled) })
			}
		}
	}))
	u.URL = s.URL
	u.Client = &http.Client{Transport: &trackingTransport{base: s.Client().Transport, open: &u.open}}
	t.Cleanup(func() {
		s.CloseClientConnections()
		s.Close()
	})
	return u
}

// Requests counts POSTs received on the scripted route (including retries).
func (u *Upstream) Requests() int64 { return u.requests.Load() }

// Cancelled closes when the first WaitForCancel request observes its context
// ending. This does not by itself prove the caller closed its response body.
func (u *Upstream) Cancelled() <-chan struct{} { return u.cancelled }

// OpenBodies counts responses returned by Client whose Body.Close has not
// been called. Reading to EOF or cancelling a context is not a Close call.
func (u *Upstream) OpenBodies() int64 { return u.open.Load() }

type trackingTransport struct {
	base http.RoundTripper
	open *atomic.Int64
}

func (t *trackingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	res, err := t.base.RoundTrip(r)
	if err == nil {
		t.open.Add(1)
		res.Body = &trackedBody{ReadCloser: res.Body, open: t.open}
	}
	return res, err
}

type trackedBody struct {
	io.ReadCloser
	open *atomic.Int64
	once sync.Once
	err  error
}

func (b *trackedBody) Close() error {
	b.once.Do(func() {
		b.err = b.ReadCloser.Close()
		b.open.Add(-1)
	})
	return b.err
}
