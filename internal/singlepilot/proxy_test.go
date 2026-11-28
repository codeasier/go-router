package singlepilot

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"net/url"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

func TestStrictProxyOrigin(t *testing.T) {
	for _, raw := range []string{"http://localhost", "http://127.0.0.1:8080/", "https://edge.proxy.test:443", "http://[::1]:3128"} {
		u, err := strictProxyOrigin(raw)
		if err != nil || u.String() != strings.TrimSuffix(raw, "/") {
			t.Fatalf("valid origin %q", raw)
		}
	}
	for _, raw := range []string{"", " ", "http://", " http://p", "socks5://p", "http://p?", "https://p#", "https://p/x",
		"https://p:0", "http://p:", "https://p:65536", "https://p:+80", "http://a..b", "http://-p", "http://a_b",
		"http://[example]", "http://[127.0.0.1]", "http://::1", "https://[fe80::1%25lo0]", "http://user:secret@proxy",
		"https://@p", "https://p/%2f", "http://host\\xyz", "https://p/secret", "https://" + strings.Repeat("a", 2048)} {
		if u, err := strictProxyOrigin(raw); u != nil || err != errStrictProxy {
			t.Errorf("did not fail closed with a constant diagnostic")
		}
	}
}

func TestStrictProxySetupDeadline(t *testing.T) {
	const limit = 500 * time.Millisecond
	proxy, seen := startHeldProxy(t)
	transport := newStrictProxyTransport(proxy, limit)
	// No cancel/deadline on the request: the independent setup limit must end
	// the attempt and close the socket, not just return to the caller.
	finished := pendingProxyRequest(t, context.Background(), transport)
	peer := nextHeldConnect(t, seen)
	select {
	case err := <-finished:
		var timeout net.Error
		if !errors.As(err, &timeout) || !timeout.Timeout() {
			t.Fatalf("setup did not fail as a timeout: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("setup deadline did not end CONNECT")
	}
	select {
	case err := <-peer:
		if !errors.Is(err, io.EOF) {
			t.Fatalf("setup deadline did not close the peer socket: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("pending CONNECT socket was not closed")
	}
}

func TestStrictProxyConnectionSlots(t *testing.T) {
	proxy, seen := startHeldProxy(t)
	transport := newStrictProxyTransport(proxy, 10*time.Second)
	batch, cancelBatch := context.WithCancel(context.Background())
	defer cancelBatch()
	first, cancelFirst := context.WithCancel(batch)
	defer cancelFirst()
	firstFinished := pendingProxyRequest(t, first, transport)
	firstPeer := nextHeldConnect(t, seen)
	for i := 1; i < strictProxyMaxConnections; i++ {
		pendingProxyRequest(t, batch, transport)
	}
	for i := 1; i < strictProxyMaxConnections; i++ {
		nextHeldConnect(t, seen)
	}
	short := transport // Same live socket pool, shorter setup budget for one request.
	short.timeout = 100 * time.Millisecond
	queued := pendingProxyRequest(t, batch, short)
	select {
	case <-seen:
		t.Fatal("strict proxy exceeded its connection cap")
	case err := <-queued:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("queue did not count against the setup deadline: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("setup deadline did not end the queued request")
	}
	pendingProxyRequest(t, batch, transport)
	select {
	case <-seen:
		t.Fatal("strict proxy exceeded its connection cap")
	case <-time.After(75 * time.Millisecond):
	}
	cancelFirst()
	select {
	case err := <-firstFinished:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("unexpected canceled attempt result: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("attempt did not return on cancellation")
	}
	select {
	case err := <-firstPeer:
		if !errors.Is(err, io.EOF) {
			t.Fatalf("canceled socket not released: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancellation did not close the first CONNECT")
	}
	// The already-queued request may now acquire the released socket slot.
	nextHeldConnect(t, seen)
}

func TestStrictProxyHandoffReleasesSetupLimits(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		wire, peer := net.Pipe()
		defer peer.Close()
		caller, cancel := context.WithCancel(context.Background())
		defer cancel()
		d := time.Now().Add(time.Second)
		if err := wire.SetDeadline(d); err != nil {
			t.Fatal(err)
		}
		socket := &strictProxySocket{Conn: wire, deadline: d, stop: context.AfterFunc(caller, func() { _ = wire.Close() })}
		defer socket.Close()
		proxyTLS := tls.Client(socket, &tls.Config{})
		upstreamTLS := tls.Client(proxyTLS, &tls.Config{})
		finishStrictProxySetup(caller, httptrace.GotConnInfo{Conn: upstreamTLS})
		// After the TLS handoff, the setup hook no longer owns the raw socket.
		// The normal net/http request owns cancellation of active requests.
		cancel()
		time.Sleep(2 * time.Second)
		done := make(chan error, 1)
		go func() {
			_, err := io.ReadFull(peer, make([]byte, 1))
			done <- err
		}()
		if _, err := socket.Write([]byte{'x'}); err != nil {
			t.Fatalf("setup deadline or close hook survived handoff: %v", err)
		}
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	})
}

func startHeldProxy(t *testing.T) (*url.URL, <-chan (<-chan error)) {
	t.Helper()
	seen := make(chan (<-chan error), strictProxyMaxConnections+1)
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect || r.Host != "127.0.0.1:1" {
			w.WriteHeader(400)
			return
		}
		c, rw, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		defer c.Close()
		if err := c.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
			return
		}
		result := make(chan error, 1)
		seen <- result
		_, err = rw.ReadByte()
		result <- err
	}))
	t.Cleanup(s.Close)
	u, err := url.Parse(s.URL)
	if err != nil {
		t.Fatal(err)
	}
	return u, seen
}

func pendingProxyRequest(t *testing.T, ctx context.Context, tr strictProxyTransport) <-chan error {
	t.Helper()
	// Never contact an external target. The fixture never opens the tunnel.
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://127.0.0.1:1/chat/completions", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	req.GetBody = nil
	result := make(chan error, 1)
	go func() {
		resp, err := tr.RoundTrip(req)
		if resp != nil {
			resp.Body.Close()
		}
		result <- err
	}()
	return result
}

func nextHeldConnect(t *testing.T, seen <-chan (<-chan error)) <-chan error {
	t.Helper()
	select {
	case peer := <-seen:
		return peer
	case <-time.After(2 * time.Second):
		t.Fatal("fixture did not observe the expected CONNECT")
		return nil
	}
}
