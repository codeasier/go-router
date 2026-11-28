package singlepilot

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/codeasier/go-router/internal/attemptlog"
	"github.com/codeasier/go-router/internal/openaichat"
)

// Test-only dialer maps the immutable registry authority to the local TLS
// fixture. Production code has no native endpoint or trust override.
func TestVolceNativeLoopbackTLS(t *testing.T) {
	registry, err := url.Parse(openaichat.VolceChatBaseURL)
	if err != nil {
		t.Fatal(err)
	}
	const jsonReply = `{"object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"}}]}`
	frames := "data: " + `{"object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"ok"}}]}` + "\n\ndata: [DONE]\n\n"
	var calls, records atomic.Int32
	peer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if n != records.Load() || r.Method != "POST" || r.TLS == nil || r.Host != registry.Host || r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer test-native-secret" || r.Header.Get("Cookie") != "" {
			t.Error("unexpected native TLS request or dispatch before intent")
		}
		b, e := io.ReadAll(r.Body)
		if e != nil || !strings.Contains(string(b), `"model":"kimi-k3"`) || !strings.Contains(string(b), `"temperature":1`) {
			t.Error("unexpected native body")
		}
		switch n {
		case 1:
			if r.Header.Get("Accept") != "*/*" {
				t.Error("unexpected JSON Accept")
			}
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, jsonReply)
		case 2:
			if r.Header.Get("Accept") != "text/event-stream" {
				t.Error("unexpected SSE Accept")
			}
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, frames)
		default:
			w.Header().Set("Location", "https://unrouted.invalid/")
			w.WriteHeader(http.StatusTemporaryRedirect)
		}
	}))
	defer peer.Close()
	tr := peer.Client().Transport.(*http.Transport).Clone()
	defer tr.CloseIdleConnections()
	tr.TLSClientConfig.ServerName = "example.com" // httptest's verified certificate SAN
	tr.Proxy = nil
	tr.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		if addr != registry.Host+":443" {
			t.Errorf("unexpected dial address %q", addr)
			return nil, net.ErrClosed
		}
		return (&net.Dialer{}).DialContext(ctx, network, peer.Listener.Addr().String())
	}
	j := journalFunc(func(in attemptlog.Intent) error {
		if in.Provider != "volceapi" || in.Model != "volceapi/kimi-k3" || in.ConnectionID != in.Provider {
			t.Error("unexpected journal route")
		}
		records.Add(1)
		return nil
	})
	h, err := connectionRoutes(j, tr, 2*time.Second, Volce([]string{"kimi-k3"}, "test-native-secret"))
	if err != nil {
		t.Fatal(err)
	}
	for i, tc := range []struct {
		body         string
		status       int
		media, token string
	}{
		{`{"model":"volceapi/kimi-k3","stream":false,"temperature":0.5}`, 200, "application/json", "ok"},
		{`{"model":"volceapi/kimi-k3","stream":true}`, 200, "text/event-stream", "[DONE]"},
		{`{"model":"volceapi/kimi-k3","stream":false}`, 502, "application/json", "invalid upstream"},
		{`{"model":"volceapi/other"}`, 404, "application/json", "unknown model"},
	} {
		r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(tc.body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Cookie", "discard")
		r.Header.Set("Authorization", "Bearer caller")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.status || w.Header().Get("Content-Type") != tc.media || !strings.Contains(w.Body.String(), tc.token) || w.Header().Get("Location") != "" {
			t.Fatalf("case %d returned %d %s", i, w.Code, w.Body.String())
		}
	}
	if calls.Load() != 3 || records.Load() != 3 {
		t.Fatal("expected exactly three dispatched requests, with no fallback or redirect")
	}
}

func TestVolceStartupRejectsEveryUnrecordableRouteAndCrossConnectionKey(t *testing.T) {
	for _, tc := range []struct {
		name    string
		generic []string
		volce   []string
	}{
		{"invalid second native ID", []string{"demo"}, []string{"kimi-k3", "bad@revision"}},
		{"native key in generic catalog", []string{"native-secret"}, []string{"kimi-k3"}},
		{"generic key in native catalog", []string{"demo"}, []string{"generic-secret"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, err := Generic("custom", tc.generic, "https://example.invalid", "generic-secret")
			if err != nil {
				t.Fatal(err)
			}
			h, err := NewConnections(journalFunc(func(attemptlog.Intent) error { t.Fatal("unexpected startup journal write"); return nil }), c, Volce(tc.volce, "native-secret"))
			if h != nil || err == nil {
				t.Fatal("unsafe route accepted")
			}
		})
	}
}
