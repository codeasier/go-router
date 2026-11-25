//go:build linux || darwin

package syntheticchat

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/codeasier/go-router/internal/attemptlog"
	"github.com/codeasier/go-router/internal/catalog"
	"github.com/codeasier/go-router/internal/testupstream"
)

const (
	plainInput  = `{"model":"fx/alias","messages":[{"role":"user","content":"synthetic prompt"}],"custom":{"retained":true}}`
	streamInput = `{"model":"fx/alias","stream":true,"stream_options":{"include_usage":true}}`
)

func durableLog(t *testing.T) *attemptlog.Journal {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	j, err := attemptlog.Create(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = j.Close() })
	return j
}

func registry(t *testing.T) *catalog.Registry {
	t.Helper()
	c, err := catalog.New([]catalog.ProviderSpec{{
		ID: "fixture", Aliases: []string{"fx"}, Transport: fixtureTransport,
		Models: []catalog.ModelSpec{
			{ID: "demo/chat", Aliases: []string{"alias"}},
			{ID: "disabled", Transport: "not-a-fixture"},
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func startGateway(t *testing.T, origin string, tr http.RoundTripper, j IntentLog) (*httptest.Server, <-chan Observation) {
	t.Helper()
	observed := make(chan Observation, 4)
	h, err := New(Fixture{
		Origin: origin, Transport: tr, Registry: registry(t), Journal: j,
		Observe: func(o Observation) { observed <- o },
	})
	if err != nil {
		t.Fatal(err)
	}
	g := httptest.NewServer(h)
	t.Cleanup(g.Close)
	return g, observed
}

func send(t *testing.T, g *httptest.Server, in string) (int, string, http.Header) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, g.URL+chatPath, strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer synthetic-client-only")
	req.Header.Set("Idempotency-Key", "client-provided")
	res, err := g.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return res.StatusCode, string(b), res.Header
}

func observation(t *testing.T, c <-chan Observation) Observation {
	t.Helper()
	select {
	case o := <-c:
		return o
	case <-time.After(4 * time.Second):
		t.Fatal("no dispatch observation")
		return Observation{}
	}
}

type recorderFunc func(attemptlog.Intent) error

func (f recorderFunc) RecordIntent(in attemptlog.Intent) error { return f(in) }

func TestDurableIntentPrecedesWireRequest(t *testing.T) {
	j := durableLog(t)
	var receipt atomic.Value
	watched := recorderFunc(func(i attemptlog.Intent) error {
		err := j.RecordIntent(i)
		if err == nil {
			receipt.Store(i)
		}
		return err
	})
	var count atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count.Add(1)
		if r.Method != http.MethodPost || r.URL.Path != chatPath || r.Header.Get("Authorization") != "" || r.Header.Get("Idempotency-Key") != "" {
			t.Error("request escaped the fixed route or forwarded client-only headers")
		}
		b, err := io.ReadAll(r.Body)
		var got struct {
			Model  string
			Custom struct{ Retained bool }
		}
		if err != nil || json.Unmarshal(b, &got) != nil || got.Model != "demo/chat" || !got.Custom.Retained {
			t.Error("wire request did not resolve canonical model and preserve unknown fields")
		}
		if i, ok := receipt.Load().(attemptlog.Intent); ok {
			rec, found, err := j.Lookup(i.AttemptID)
			if err != nil || !found || rec.Intent != i || rec.Intent.BodyDigest != sha256.Sum256(b) ||
				rec.Intent.Model != "fixture/demo/chat" || rec.Intent.Provider != "fixture" {
				t.Error("upstream did not find the committed intent for the exact wire bytes")
			}
		} else {
			t.Error("upstream started before an intent receipt")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"object":"chat.completion","choices":[{"message":{"role":"assistant","content":"hello"}}],"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}}`)
	}))
	defer up.Close()
	tr := &trackingTransport{base: up.Client().Transport}
	g, c := startGateway(t, up.URL, tr, watched)
	s, b, headers := send(t, g, plainInput)
	o := observation(t, c)
	if s != http.StatusOK || !strings.Contains(b, "hello") || o.Execution != Executed || !o.Complete ||
		o.Usage == nil || *o.Usage != (Usage{2, 1, 3}) || o.RequestID != headers.Get("X-Request-ID") ||
		count.Load() != 1 || tr.open.Load() != 0 {
		t.Fatalf("unexpected JSON outcome: HTTP %d %#v; requests=%d bodies=%d", s, o, count.Load(), tr.open.Load())
	}
}

type trackingTransport struct {
	base http.RoundTripper
	open atomic.Int32
}

func (t *trackingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.GetBody != nil {
		return nil, errors.New("fixture refuses rewindable request")
	}
	resp, err := t.base.RoundTrip(r)
	if err == nil {
		t.open.Add(1)
		resp.Body = &trackedBody{ReadCloser: resp.Body, open: &t.open}
	}
	return resp, err
}

type trackedBody struct {
	io.ReadCloser
	open *atomic.Int32
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

func TestJournalFailurePreventsAnyTransport(t *testing.T) {
	closed := durableLog(t)
	if err := closed.Close(); err != nil {
		t.Fatal(err)
	}
	// The exact prerequisite's component tests exercise actual short-write and
	// fsync injection. Here those dependency failures exercise the public HTTP
	// boundary, as does a real journal already in a failed/unavailable state.
	for name, j := range map[string]IntentLog{
		"write":  recorderFunc(func(attemptlog.Intent) error { return errors.New("synthetic write failure") }),
		"sync":   recorderFunc(func(attemptlog.Intent) error { return errors.New("synthetic sync failure") }),
		"closed": closed,
	} {
		t.Run(name, func(t *testing.T) {
			up := testupstream.Start(t, testupstream.Completion)
			g, c := startGateway(t, up.URL, up.Client.Transport, j)
			s, b, _ := send(t, g, plainInput)
			o := observation(t, c)
			if s != http.StatusServiceUnavailable || !strings.Contains(b, "dispatch_unavailable") || strings.Contains(b, "failure") ||
				o.Execution != NotSent || o.Complete || o.Usage != nil || up.Requests() != 0 || up.OpenBodies() != 0 {
				t.Fatalf("journal failure leaked or dispatched: HTTP %d %s %#v, requests=%d", s, b, o, up.Requests())
			}
		})
	}
}

const (
	firstData = "data: {\"object\":\"chat.completion.chunk\",\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\n"
	lastData  = "data: {\"object\":\"chat.completion.chunk\",\"choices\":[{\"finish_reason\":\"stop\"}]}\n\n"
	usageData = "data: {\"object\":\"chat.completion.chunk\",\"choices\":[],\"usage\":{\"prompt_tokens\":2,\"completion_tokens\":1,\"total_tokens\":3}}\n\n"
	doneData  = "data: [DONE]\n\n"
)

func TestSSETerminationAndUsage(t *testing.T) {
	for _, tc := range []struct {
		name, wire      string
		complete, usage bool
	}{
		{"final-usage", firstData + lastData + usageData + doneData, true, true},
		{"crlf", strings.ReplaceAll(firstData+lastData+usageData+doneData, "\n", "\r\n"), true, true},
		{"missing-usage", firstData + lastData + doneData, true, false},
		{"malformed-usage", firstData + lastData + strings.Replace(usageData, `"total_tokens":3`, `"total_tokens":4`, 1) + doneData, true, false},
		{"clean-eof", firstData + lastData, false, false},
		{"clean-eof-after-usage", firstData + lastData + usageData, false, false},
		{"half-done-frame", firstData + lastData + "data: [DONE]\n", false, false},
		{"choice-after-usage", firstData + usageData + lastData + doneData, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var requests atomic.Int32
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, tc.wire)
			}))
			defer up.Close()
			tr := &trackingTransport{base: up.Client().Transport}
			g, c := startGateway(t, up.URL, tr, durableLog(t))
			s, b, h := send(t, g, streamInput)
			o := observation(t, c)
			if s != http.StatusOK || h.Get("Content-Type") != "text/event-stream" || o.Complete != tc.complete ||
				(o.Usage != nil) != tc.usage || requests.Load() != 1 || tr.open.Load() != 0 {
				t.Fatalf("stream result: HTTP %d, %#v requests=%d open=%d body=%s", s, o, requests.Load(), tr.open.Load(), b)
			}
			if tc.complete {
				if b != tc.wire || o.Execution != Executed {
					t.Fatal("confirmed completion did not preserve upstream frames")
				}
				if tc.usage && *o.Usage != (Usage{2, 1, 3}) {
					t.Fatalf("incorrect usage: %#v", o.Usage)
				}
			} else if o.Execution != Unknown || strings.Contains(b, "[DONE]") || !strings.Contains(b, "event: error\ndata:") {
				t.Fatal("interrupted stream fabricated a success marker or lacked a terminal error")
			}
		})
	}
}

func TestNoReplayOrRedirect(t *testing.T) {
	for _, mode := range []testupstream.Mode{testupstream.Unavailable, testupstream.InterruptedStream} {
		up := testupstream.Start(t, mode)
		g, c := startGateway(t, up.URL, up.Client.Transport, durableLog(t))
		input := plainInput
		if mode == testupstream.InterruptedStream {
			input = streamInput
		}
		_, _, _ = send(t, g, input)
		o := observation(t, c)
		if o.Execution != Unknown || o.Complete || o.Usage != nil || up.Requests() != 1 || up.OpenBodies() != 0 {
			t.Fatalf("retried, misclassified, or leaked failed mode %v: %#v, requests=%d bodies=%d", mode, o, up.Requests(), up.OpenBodies())
		}
	}
	var calls atomic.Int32
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Location", "/must-not-follow")
		w.WriteHeader(http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()
	g, c := startGateway(t, redirect.URL, redirect.Client().Transport, durableLog(t))
	s, _, _ := send(t, g, plainInput)
	o := observation(t, c)
	if s != http.StatusBadGateway || calls.Load() != 1 || o.Execution != Unknown {
		t.Fatalf("unexpected redirect behavior: HTTP %d, %d calls, %#v", s, calls.Load(), o)
	}
}

func TestClientCancellationClosesActualUpstreamBody(t *testing.T) {
	up := testupstream.Start(t, testupstream.WaitForCancel)
	g, c := startGateway(t, up.URL, up.Client.Transport, durableLog(t))
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.URL+chatPath, strings.NewReader(streamInput))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := g.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	first, err := bufio.NewReader(res.Body).ReadString('\n')
	if err != nil || !strings.HasPrefix(first, "data: ") || up.OpenBodies() != 1 {
		t.Fatal("first streamed frame was not flushed with an open upstream body")
	}
	cancel()
	select {
	case <-up.Cancelled():
	case <-time.After(3 * time.Second):
		t.Fatal("upstream never saw canceled request context")
	}
	o := observation(t, c)
	if o.Execution != Unknown || o.Complete || o.Usage != nil || up.Requests() != 1 || up.OpenBodies() != 0 {
		t.Fatalf("canceled stream was replayed or its body was not closed: %#v requests=%d open bodies=%d", o, up.Requests(), up.OpenBodies())
	}
}

func TestFixtureRejectsNonLoopbackAndNonFixtureTransport(t *testing.T) {
	reg := registry(t)
	up := testupstream.Start(t, testupstream.Completion)
	j := durableLog(t)
	for _, invalid := range []string{"http://example.invalid:1234", "http://localhost:1234", "http://127.0.0.1:1234/path", "http://127.0.0.1:1234?x=1", "http://user:pass@127.0.0.1:1234", "https://127.0.0.1:1234"} {
		if _, err := New(Fixture{Origin: invalid, Transport: up.Client.Transport, Registry: reg, Journal: j}); err == nil {
			t.Errorf("accepted disallowed origin %q", invalid)
		}
	}
	g, _ := startGateway(t, up.URL, up.Client.Transport, j)
	s, _, _ := send(t, g, `{"model":"fixture/disabled"}`)
	if s != http.StatusNotFound || up.Requests() != 0 {
		t.Fatal("non-fixture model attempted dispatch")
	}
}

func TestDuplicateUsageIsUnknown(t *testing.T) {
	const zero = `{"prompt_tokens":0,"completion_tokens":0,"total_tokens":0}`
	for _, tc := range []struct {
		name, usage string
		known       bool
	}{
		{"explicit-zero", `"usage":` + zero, true},
		{"same-value", `"usage":{"prompt_tokens":0,"prompt_tokens":0,"completion_tokens":0,"total_tokens":0}`, false},
		{"nested-escaped", `"usage":{"prompt_tokens":9,"\u0070rompt_tokens":0,"completion_tokens":0,"total_tokens":0}`, false},
		{"nested-case", `"usage":{"prompt_tokens":0,"completion_tokens":9,"COMPLETION_TOKENS":0,"total_tokens":0}`, false},
		{"nested-total", `"usage":{"prompt_tokens":0,"completion_tokens":0,"total_tokens":9,"total_tokens":0}`, false},
		{"duplicate-container", `"usage":null,"usage":` + zero, false},
		{"container-case", `"USAGE":null,"usage":` + zero, false},
		{"duplicate-extension-only", `"usage":{"prompt_tokens":0,"completion_tokens":0,"total_tokens":0,"ext":1,"ext":2}`, true},
	} {
		for _, streaming := range []bool{false, true} {
			mode := "json"
			if streaming {
				mode = "sse"
			}
			t.Run(mode+"/"+tc.name, func(t *testing.T) {
				input := plainInput
				contentType := "application/json"
				wire := `{"object":"chat.completion","choices":[{"message":{"content":"hello"}}],` + tc.usage + "}"
				if streaming {
					input = streamInput
					contentType = "text/event-stream"
					wire = firstData + lastData + `data: {"object":"chat.completion.chunk","choices":[],` + tc.usage + "}\n\n" + doneData
				}
				up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Content-Type", contentType)
					_, _ = io.WriteString(w, wire)
				}))
				defer up.Close()
				g, c := startGateway(t, up.URL, up.Client().Transport, durableLog(t))
				s, body, _ := send(t, g, input)
				o := observation(t, c)
				if s != http.StatusOK || body != wire || o.Execution != Executed || !o.Complete {
					t.Fatalf("usage should not alter otherwise complete payload: HTTP %d %#v", s, o)
				}
				if (o.Usage != nil) != tc.known || (tc.known && *o.Usage != (Usage{})) {
					t.Fatalf("wanted known=%v and explicit zero if known, got %#v", tc.known, o.Usage)
				}
			})
		}
	}
}

func TestCanonicalCatalogIDsMatchRealJournal(t *testing.T) {
	up := testupstream.Start(t, testupstream.Completion)
	j := durableLog(t)
	for _, bad := range []struct {
		name, provider, model string
	}{
		{"provider-at", "fixture@2", "demo/chat"},
		{"model-at", "fixture", "demo/chat@v1"},
		{"provider-too-long", strings.Repeat("p", 129), "chat"},
		{"canonical-too-long", "fixture", strings.Repeat("m", 121)},
	} {
		t.Run(bad.name, func(t *testing.T) {
			reg, err := catalog.New([]catalog.ProviderSpec{{
				ID: bad.provider, Transport: fixtureTransport,
				Models: []catalog.ModelSpec{{ID: bad.model}},
			}})
			if err != nil {
				t.Fatalf("fixture must use catalog-valid metadata: %v", err)
			}
			if h, err := New(Fixture{Origin: up.URL, Transport: up.Client.Transport, Registry: reg, Journal: j}); err == nil || h != nil {
				t.Fatal("fixture accepted an enabled canonical ID that the journal cannot record")
			}
		})
	}
	if up.Requests() != 0 {
		t.Fatal("fixture startup attempted an upstream request")
	}
	const provider = "fixture"
	model := strings.Repeat("m", 128-len(provider)-1)
	reg, err := catalog.New([]catalog.ProviderSpec{{
		ID: provider, Aliases: []string{"fx@2"}, Transport: fixtureTransport,
		Models: []catalog.ModelSpec{
			{ID: model, Aliases: []string{"alias@2"}},
			{ID: "model@v1", Transport: "disabled-provider"},
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	observed := make(chan Observation, 1)
	h, err := New(Fixture{Origin: up.URL, Transport: up.Client.Transport, Registry: reg, Journal: j,
		Observe: func(o Observation) { observed <- o }})
	if err != nil {
		t.Fatalf("safe canonical IDs should be accepted even with alias/disabled metadata outside journal grammar: %v", err)
	}
	g := httptest.NewServer(h)
	t.Cleanup(g.Close)
	s, _, _ := send(t, g, `{"model":"fx@2/alias@2"}`)
	o := observation(t, observed)
	found, ok, err := j.Lookup(o.AttemptID)
	if s != http.StatusOK || o.Execution != Executed || !ok || err != nil ||
		found.Intent.Provider != provider || found.Intent.Model != provider+"/"+model || up.Requests() != 1 {
		t.Fatalf("valid canonical identity did not reach upstream after real journal record: HTTP %d %#v %#v %v", s, o, found, err)
	}
}
