package controlplane

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type sourceFunc func(context.Context) ([]NodeSnapshot, error)

func (f sourceFunc) Snapshots(ctx context.Context) ([]NodeSnapshot, error) {
	return f(ctx)
}

func ptr(s string) *string { return &s }

func sample(id, applied string, seen time.Time) NodeSnapshot {
	return NodeSnapshot{
		NodeID: id, DesiredRevision: ptr("rev-7"), LastSeen: seen,
		LastStatus: &NodeStatus{NodeID: id, Ready: true, AppliedRevision: ptr(applied)},
	}
}

func request(t *testing.T, client *http.Client, method, url string) (int, string, http.Header) {
	t.Helper()
	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, string(body), resp.Header
}

func TestDashboardHTTP(t *testing.T) {
	now := time.Now().UTC()
	b, a := sample("node-b", "rev-6", now), sample("node-a", "rev-7", now)
	input := []NodeSnapshot{b, a}
	handler := NewDashboard(sourceFunc(func(context.Context) ([]NodeSnapshot, error) {
		return input, nil
	}), time.Minute)
	srv := httptest.NewServer(handler)
	defer srv.Close()

	code, body, h := request(t, srv.Client(), "GET", srv.URL)
	if code != http.StatusOK || h.Get("Content-Type") != "text/html; charset=utf-8" || h.Get("Cache-Control") != "no-store" {
		t.Fatalf("GET returned %d %v", code, h)
	}
	one := `<th scope="row">node-a</th><td>Ready</td><td>rev-7</td><td>rev-7</td><td>Matched</td>`
	two := `<th scope="row">node-b</th><td>Ready</td><td>rev-7</td><td>rev-6</td><td>Drift</td>`
	if strings.Count(body, one) != 1 || strings.Count(body, two) != 1 || strings.Index(body, one) >= strings.Index(body, two) {
		t.Fatalf("missing or misordered independent observations: %s", body)
	}
	if input[0].NodeID != "node-b" {
		t.Fatal("handler sorted its source in place")
	}
	code, body, _ = request(t, srv.Client(), "HEAD", srv.URL)
	if code != http.StatusOK || body != "" {
		t.Fatalf("HEAD returned %d, %q", code, body)
	}
}

func TestObservationStates(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		name       string
		change     func(*NodeSnapshot)
		state      string
		applied    string
		comparison string
	}{
		{"fresh-boundary", func(n *NodeSnapshot) { n.LastSeen = now.Add(-time.Minute) }, "Ready", "rev-7", "Matched"},
		{"old", func(n *NodeSnapshot) { n.LastSeen = now.Add(-time.Minute - time.Nanosecond) }, "Stale", "rev-7", "Unverified"},
		{"last-check-failed", func(n *NodeSnapshot) { n.Offline = true }, "Offline", "rev-7", "Unverified"},
		{"offline-no-history", func(n *NodeSnapshot) { n.Offline = true; n.LastStatus = nil }, "Offline", "Unknown", "Unverified"},
		{"unknown", func(n *NodeSnapshot) { n.LastStatus = nil }, "Not observed", "Unknown", "Unverified"},
		{"no-revision", func(n *NodeSnapshot) { n.LastStatus.AppliedRevision = nil }, "Ready", "Unknown", "Unverified"},
		{"not-ready", func(n *NodeSnapshot) { n.LastStatus.Ready = false }, "Not ready", "rev-7", "Matched"},
		{"wrong-node", func(n *NodeSnapshot) { n.LastStatus.NodeID = "different" }, "Invalid observation", "Unknown", "Unverified"},
		{"future", func(n *NodeSnapshot) { n.LastSeen = now.Add(time.Second) }, "Invalid observation", "Unknown", "Unverified"},
		{"missing-time", func(n *NodeSnapshot) { n.LastSeen = time.Time{} }, "Invalid observation", "Unknown", "Unverified"},
		{"unsafe-revision", func(n *NodeSnapshot) { n.LastStatus.AppliedRevision = ptr("https://token@example.invalid/a/<script>") }, "Ready", "Unknown", "Unverified"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			n := sample("node", "rev-7", now)
			tc.change(&n)
			h := dashboard(sourceFunc(func(context.Context) ([]NodeSnapshot, error) {
				return []NodeSnapshot{n}, nil
			}), time.Minute, func() time.Time { return now })
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
			body := w.Body.String()
			if w.Code != http.StatusOK || !strings.Contains(body, "<td>"+tc.state+"</td><td>rev-7</td><td>"+tc.applied+"</td><td>"+tc.comparison+"</td>") || strings.Contains(body, "token@example") {
				t.Fatalf("unexpected view (%d): %s", w.Code, body)
			}
		})
	}
}

func TestDashboardAdmissionAndFailure(t *testing.T) {
	var calls int
	priv := "https://token@example.invalid/?secret=private"
	source := sourceFunc(func(context.Context) ([]NodeSnapshot, error) {
		calls++
		return nil, errors.New(priv)
	})
	h := NewDashboard(source, time.Minute)
	for _, method := range []string{"POST", "PUT", "PATCH", "DELETE"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(method, "/", nil))
		if w.Code != http.StatusMethodNotAllowed || w.Header().Get("Allow") != "GET, HEAD" {
			t.Fatalf("%s: %d", method, w.Code)
		}
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/admin/v1/status", nil))
	if w.Code != http.StatusNotFound || calls != 0 {
		t.Fatalf("unexpected source access before admission: %d, calls: %d", w.Code, calls)
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	if w.Code != http.StatusServiceUnavailable || strings.Contains(w.Body.String(), "token@") || calls != 1 {
		t.Fatalf("unsafe or unexpected source error: %s (%d calls)", w.Body.String(), calls)
	}
}

func TestInventoryIntegrity(t *testing.T) {
	now := time.Now()
	valid := sample("node", "rev-7", now)
	tests := []struct {
		name  string
		nodes []NodeSnapshot
		code  int
		text  string
	}{
		{"empty", nil, http.StatusOK, "No gateways registered."},
		{"duplicate", []NodeSnapshot{valid, valid}, http.StatusServiceUnavailable, "Fleet inventory invalid"},
		{"unsafe-id", []NodeSnapshot{{NodeID: `<img src="https://bearer@example.invalid">`}}, http.StatusServiceUnavailable, "Fleet inventory invalid"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := NewDashboard(sourceFunc(func(context.Context) ([]NodeSnapshot, error) { return tc.nodes, nil }), time.Minute)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
			body := w.Body.String()
			if w.Code != tc.code || !strings.Contains(body, tc.text) || strings.Contains(body, "<img") || strings.Contains(body, "bearer@") {
				t.Fatalf("%d %s", w.Code, body)
			}
		})
	}
}
