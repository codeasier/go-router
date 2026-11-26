package openaichat

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/codeasier/go-router/internal/attemptlog"
)

type countedBody struct {
	io.Reader
	closes int
}

func (b *countedBody) Close() error {
	b.closes++
	return nil
}

func testPrepared(t *testing.T, stream bool) *Prepared {
	t.Helper()
	a, err := New("https://fixture.invalid/v1")
	if err != nil {
		t.Fatal(err)
	}
	p, err := a.Prepare("catalog/model", []byte(fmt.Sprintf(`{"stream":%t}`, stream)))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestOfflineCustomProviderFixture(t *testing.T) {
	for _, tc := range []struct {
		name   string
		file   string
		media  string
		stream bool
	}{
		{"json", "custom-completion.json", "application/json", false},
		{"sse", "custom-completion.sse", "text/event-stream", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body, err := os.ReadFile(filepath.Join("testdata", tc.file))
			if err != nil {
				t.Fatal(err)
			}
			source := &countedBody{Reader: bytes.NewReader(body)}
			res := &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {tc.media}}, Body: source}
			var output bytes.Buffer
			got, err := testPrepared(t, tc.stream).Decode(context.Background(), res, func(b []byte) error {
				_, err := output.Write(b)
				return err
			})
			if err != nil || got.HTTPStatus != 200 || !got.Complete || got.Usage == nil ||
				*got.Usage != (Usage{PromptTokens: 8, CompletionTokens: 3, TotalTokens: 11}) || source.closes != 1 {
				t.Fatalf("observation: %+v, error: %v, close count: %d", got, err, source.closes)
			}
			want := body
			if tc.stream {
				want = bytes.TrimPrefix(body, []byte(": synthetic fixture\n\n"))
				want = bytes.TrimSuffix(want, []byte(": fixture comment after terminal\n"))
			}
			if !bytes.Equal(output.Bytes(), want) {
				t.Fatal("validated fixture payload changed")
			}
		})
	}
}

func TestSanitizedErrorsAndUncertainUsage(t *testing.T) {
	const (
		jsonChoices = `{"object":"chat.completion","choices":[{}]}`
		sseChoice   = "data: " + `{"object":"chat.completion.chunk","choices":[{}]}` + "\r\n\r\n"
		sseUsage    = "data: " + `{"object":"chat.completion.chunk","choices":[],"usage":{"prompt_tokens":0,"completion_tokens":0,"total_tokens":0}}` + "\r\n\r\n"
		sseEnd      = "data: [DONE]\r\n\r\n"
		bad         = `{"error":{"message":"secret fixture diagnostic"}}`
		dup         = `{"object":"chat.completion","choices":[{}],"usage":null,"Usage":{"prompt_tokens":0,"completion_tokens":0,"total_tokens":0}}`
	)
	cases := []struct {
		name, body string
		status     int
		stream     bool
		err        error
		want       string
	}{
		{"missing usage", jsonChoices, 200, false, nil, jsonChoices},
		{"duplicate usage", dup, 200, false, nil, dup},
		{"non-success response", bad, 429, false, ErrHTTPStatus, ""},
		{"redirect", bad, 307, false, ErrHTTPStatus, ""},
		{"JSON error", bad, 200, false, ErrUpstreamError, ""},
		{"oversize", strings.Repeat("x", maxResponse+1), 200, false, ErrInvalidResponse, ""},
		{"SSE missing usage", sseChoice + sseEnd, 200, true, nil, sseChoice + sseEnd},
		{"EOF after usage", sseChoice + sseUsage, 200, true, ErrIncompleteSSE, sseChoice + sseUsage},
		{"SSE no choices", sseEnd, 200, true, ErrIncompleteSSE, ""},
		{"SSE error event", sseChoice + "event: error\ndata: " + bad + "\n\n", 200, true, ErrUpstreamError, sseChoice},
		{"unnamed SSE error", sseChoice + "data: " + bad + "\n\n", 200, true, ErrUpstreamError, sseChoice},
		{"oversize frame", "data: " + strings.Repeat(" ", 65<<10) + "\n\n", 200, true, ErrIncompleteSSE, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			media := "application/json"
			if tc.stream {
				media = "text/event-stream"
			}
			body := &countedBody{Reader: strings.NewReader(tc.body)}
			res := &http.Response{StatusCode: tc.status, Header: http.Header{
				"Content-Type": {media},
				"Location":     {"https://unapproved.invalid/secret-fixture"},
			}, Body: body}
			var output bytes.Buffer
			got, err := testPrepared(t, tc.stream).Decode(context.Background(), res, func(b []byte) error {
				_, err := output.Write(b)
				return err
			})
			if !errors.Is(err, tc.err) || got.Complete != (tc.err == nil) || got.Usage != nil ||
				got.HTTPStatus != tc.status || body.closes != 1 || output.String() != tc.want ||
				strings.Contains(fmt.Sprint(err), "secret") {
				t.Fatalf("wrong observation or unsanitized response: %+v (%v)", got, err)
			}
		})
	}
}

func TestCanceledOrUndeliverableResponseClosesBody(t *testing.T) {
	p := testPrepared(t, false)
	for _, cancel := range []bool{false, true} {
		ctx, stop := context.WithCancel(context.Background())
		if cancel {
			stop()
		}
		source := &countedBody{Reader: strings.NewReader(`{"object":"chat.completion","choices":[{}]}`)}
		response := &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: source}
		calls := 0
		got, err := p.Decode(ctx, response, func([]byte) error {
			calls++
			return errors.New("hidden downstream error")
		})
		stop()
		want := ErrDelivery
		wantCalls := 1
		if cancel {
			want = context.Canceled
			wantCalls = 0
		}
		if !errors.Is(err, want) || got.Complete || source.closes != 1 || calls != wantCalls {
			t.Fatalf("did not close rejected body: %+v %v", got, err)
		}
	}
}

func TestLocalDispatcherIllustratesIntentBeforeSingleWire(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("durable journal not implemented on this platform")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	j, err := attemptlog.Create(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = j.Close() })
	var calls atomic.Int32
	var barrierWitness atomic.Bool
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		body, err := io.ReadAll(r.Body)
		if err != nil || r.URL.Path != "/team/v1/chat/completions" || r.URL.RawQuery != "" ||
			r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer offline-only" ||
			r.Header.Get("Accept") != "application/json" || r.Header.Get("Cookie") != "" {
			http.Error(w, "invalid fixture request", 400)
			return
		}
		barrierWitness.Store(verifyJournalIntent(dir, body))
		// The adapter and test dispatcher must not follow this redirect.
		w.Header().Set("Location", "/unexpected-second-request")
		w.WriteHeader(http.StatusTemporaryRedirect)
	}))
	t.Cleanup(server.Close)
	a, err := New(server.URL + "/team/v1")
	if err != nil {
		t.Fatal(err)
	}
	p, err := a.Prepare("catalog/model", []byte(`{"model":"alias","messages":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	dispatch := func(journal *attemptlog.Journal, p *Prepared) (Observation, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		r, err := p.NewRequest(ctx, "offline-only")
		if err != nil {
			return Observation{}, err
		}
		if err := journal.RecordIntent(attemptlog.Intent{
			RequestID: "fixture-request", AttemptID: "fixture-attempt", Provider: "fixture", Model: "fixture/catalog/model",
			ConnectionID: "fixture-connection", Operation: "inference", BodyDigest: p.BodyDigest(),
		}); err != nil {
			r.Body.Close()
			return Observation{}, err
		}
		res, err := server.Client().Transport.RoundTrip(r)
		if err != nil {
			return Observation{}, err
		}
		return p.Decode(ctx, res, nil)
	}
	got, err := dispatch(j, p)
	if !errors.Is(err, ErrHTTPStatus) || got.HTTPStatus != http.StatusTemporaryRedirect ||
		got.Complete || calls.Load() != 1 || !barrierWitness.Load() {
		t.Fatalf("journal/wire/redirect behavior wrong: %+v %v", got, err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := dispatch(j, p); !errors.Is(err, attemptlog.ErrUnavailable) || calls.Load() != 1 {
		t.Fatal("test dispatcher sent despite journal failure")
	}
}

func verifyJournalIntent(dir string, body []byte) bool {
	data, err := os.ReadFile(filepath.Join(dir, "dispatch-intents.v1"))
	if err != nil {
		return false
	}
	lines := bytes.Split(data, []byte("\n"))
	if len(lines) != 3 || string(lines[0]) != "go-router/dispatch-intents/v1" {
		return false
	}
	digest, record, ok := bytes.Cut(lines[1], []byte("\t"))
	if !ok {
		return false
	}
	expected := sha256.Sum256(record)
	var stored attemptlog.Record
	return string(digest) == hex.EncodeToString(expected[:]) && json.Unmarshal(record, &stored) == nil &&
		stored.Intent.AttemptID == "fixture-attempt" && stored.Intent.BodyDigest == sha256.Sum256(body)
}
