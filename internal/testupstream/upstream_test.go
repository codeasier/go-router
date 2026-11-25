package testupstream_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/codeasier/go-router/internal/testupstream"
)

// These markers are synthetic. Their absence checks that fixture replies
// don't echo request content; no private payload or credential is required.
const requestBody = `{"model":"test-model","messages":[{"role":"user","content":"synthetic-prompt-marker"}]}`

func request(t *testing.T, u *testupstream.Upstream, ctx context.Context, stream bool) *http.Response {
	t.Helper()
	body := requestBody
	if stream {
		body = strings.TrimSuffix(body, "}") + `,"stream":true}`
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.URL+"/v1/chat/completions", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer synthetic-header-marker")
	u.Client.Timeout = 10 * time.Second
	res, err := u.Client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { res.Body.Close() })
	if u.Requests() != 1 || u.OpenBodies() != 1 {
		t.Fatal("expected one upstream request and one unclosed response")
	}
	return res
}

func closeBody(t *testing.T, u *testupstream.Upstream, res *http.Response) {
	t.Helper()
	if u.OpenBodies() != 1 {
		t.Fatal("EOF or cancellation was incorrectly counted as Close")
	}
	for i := 0; i < 2; i++ {
		if err := res.Body.Close(); err != nil {
			t.Fatal(err)
		}
		if u.OpenBodies() != 0 {
			t.Fatal("response Close must be counted exactly once")
		}
	}
}

type envelope struct {
	ID, Object, Model string
	Choices           []struct {
		Message, Delta struct{ Role, Content string }
		FinishReason   *string `json:"finish_reason"`
	}
	Usage struct {
		Prompt     int `json:"prompt_tokens"`
		Completion int `json:"completion_tokens"`
		Total      int `json:"total_tokens"`
	}
	Error struct{ Type, Code, Message string }
}

func decode(t *testing.T, data string) envelope {
	t.Helper()
	if strings.Contains(data, "synthetic-prompt-marker") || strings.Contains(data, "synthetic-header-marker") {
		t.Fatal("fixture echoed a request field")
	}
	var result envelope
	if err := json.Unmarshal([]byte(data), &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestJSONScripts(t *testing.T) {
	for _, tc := range []struct {
		name string
		mode testupstream.Mode
		code int
		kind string
	}{
		{"completion", testupstream.Completion, 200, ""},
		{"rate limit", testupstream.RateLimited, 429, "rate_limit_error"},
		{"unavailability", testupstream.Unavailable, 503, "server_error"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u := testupstream.Start(t, tc.mode)
			res := request(t, u, t.Context(), false)
			if res.StatusCode != tc.code || res.Header.Get("Content-Type") != "application/json" {
				t.Fatalf("HTTP %s, content type %q", res.Status, res.Header.Get("Content-Type"))
			}
			data, err := io.ReadAll(res.Body)
			if err != nil {
				t.Fatal(err)
			}
			v := decode(t, string(data))
			if tc.kind != "" {
				if v.Error.Type != tc.kind || v.Error.Code == "" || v.Error.Message == "" || len(v.Choices) != 0 {
					t.Fatal("incorrect error envelope")
				}
			} else {
				if v.Object != "chat.completion" || v.ID != "chatcmpl-test" || v.Model != "test-model" || len(v.Choices) != 1 {
					t.Fatal("incorrect completion envelope")
				}
				c := v.Choices[0]
				if c.Message.Role != "assistant" || c.Message.Content != "hello" || c.FinishReason == nil || *c.FinishReason != "stop" {
					t.Fatal("incorrect completion choice")
				}
				if v.Usage.Prompt != 2 || v.Usage.Completion != 1 || v.Usage.Total != 3 {
					t.Fatal("incorrect usage")
				}
			}
			closeBody(t, u, res)
		})
	}
}

func firstFrame(t *testing.T, res *http.Response) *bufio.Reader {
	t.Helper()
	if res.StatusCode != http.StatusOK || res.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("HTTP %s, content type %q", res.Status, res.Header.Get("Content-Type"))
	}
	br := bufio.NewReader(res.Body)
	line, err := br.ReadString('\n')
	if err != nil || !strings.HasPrefix(line, "data: ") {
		t.Fatalf("missing first data line: %v", err)
	}
	chunk := decode(t, strings.TrimPrefix(line, "data: "))
	if chunk.Object != "chat.completion.chunk" || len(chunk.Choices) != 1 || chunk.Choices[0].Delta.Content != "hello" || chunk.Choices[0].FinishReason != nil {
		t.Fatal("incorrect first chunk")
	}
	if sep, err := br.ReadString('\n'); sep != "\n" || err != nil {
		t.Fatal("missing first event boundary")
	}
	return br
}

func TestStreamCompleted(t *testing.T) {
	u := testupstream.Start(t, testupstream.Stream)
	res := request(t, u, t.Context(), true)
	br := firstFrame(t, res)
	line, err := br.ReadString('\n')
	if err != nil || !strings.HasPrefix(line, "data: ") {
		t.Fatal("missing finish chunk")
	}
	chunk := decode(t, strings.TrimPrefix(line, "data: "))
	if len(chunk.Choices) != 1 || chunk.Choices[0].FinishReason == nil || *chunk.Choices[0].FinishReason != "stop" {
		t.Fatal("incorrect finish reason")
	}
	tail, err := io.ReadAll(br)
	if err != nil || string(tail) != "\ndata: [DONE]\n\n" {
		t.Fatalf("incorrect terminal SSE frame: %q, %v", tail, err)
	}
	closeBody(t, u, res)
}

func TestStreamInterrupted(t *testing.T) {
	u := testupstream.Start(t, testupstream.InterruptedStream)
	res := request(t, u, t.Context(), true)
	tail, err := io.ReadAll(firstFrame(t, res))
	if len(tail) != 0 || !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("want first frame then interrupted HTTP body, got %q, %v", tail, err)
	}
	closeBody(t, u, res)
}

func TestCancelPropagatesButDoesNotCountAsBodyClose(t *testing.T) {
	u := testupstream.Start(t, testupstream.WaitForCancel)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	res := request(t, u, ctx, true)
	br := firstFrame(t, res)
	select {
	case <-u.Cancelled():
		t.Fatal("upstream observed cancellation before client cancelled")
	default:
	}
	cancel()
	select {
	case <-u.Cancelled():
	case <-time.After(3 * time.Second):
		t.Fatal("upstream did not observe client cancellation")
	}
	tail, err := io.ReadAll(br)
	if len(tail) != 0 || !errors.Is(err, context.Canceled) {
		t.Fatalf("want client cancellation without terminal frame, got %q, %v", tail, err)
	}
	closeBody(t, u, res)
}

type requestBodyProbe struct {
	io.Reader
	closed atomic.Bool
}

func (p *requestBodyProbe) Close() error {
	p.closed.Store(true)
	return nil
}

func TestClientOnlyReachesItsFixture(t *testing.T) {
	u := testupstream.Start(t, testupstream.Completion)
	u.Client.Timeout = 10 * time.Second
	var otherRequests atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		otherRequests.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(other.Close)

	for _, endpoint := range []string{other.URL, "https://" + strings.TrimPrefix(u.URL, "http://")} {
		body := &requestBodyProbe{Reader: strings.NewReader(requestBody)}
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, endpoint+"/v1/chat/completions", body)
		if err != nil {
			t.Fatal(err)
		}
		res, err := u.Client.Do(req)
		if res != nil {
			res.Body.Close()
			t.Fatalf("non-fixture request returned HTTP %d", res.StatusCode)
		}
		if err == nil || !strings.Contains(err.Error(), "outside the fixture origin") {
			t.Fatalf("want explicit fixture-origin refusal, got %v", err)
		}
		if !body.closed.Load() {
			t.Fatal("rejected request body not closed")
		}
	}
	if otherRequests.Load() != 0 || u.Requests() != 0 || u.OpenBodies() != 0 {
		t.Fatal("non-fixture request reached a server or affected fixture counts")
	}

	closeBody(t, u, request(t, u, t.Context(), false))
}
