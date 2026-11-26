package controlplane

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const (
	collectorKeyA = "mock-node-a-Bearer-key-0123456789"
	collectorKeyB = "mock-node-b-Bearer-key-0123456789"
)

func localAddr(srv *httptest.Server) string { return strings.TrimPrefix(srv.URL, "http://") }

func waitSnapshots(t *testing.T, c *Collector, pred func([]NodeSnapshot) bool) []NodeSnapshot {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		rows, err := c.Snapshots(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if pred(rows) {
			return rows
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			t.Fatalf("snapshot never reached expected state: %+v", rows)
		}
	}
}

func stopCollector(t *testing.T, cancel context.CancelFunc, done <-chan error) {
	t.Helper()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run returned %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("canceled pollers did not exit")
	}
}

func TestCollectorTwoNodesAndDashboardHTTP(t *testing.T) {
	var aCalls, bCalls atomic.Int32
	var failB atomic.Bool
	serve := func(id, key, rev string, calls *atomic.Int32, fail *atomic.Bool) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet || r.URL.Path != statusPath || r.URL.RawQuery != "" || r.Header.Get("Authorization") != "Bearer "+key {
				t.Error("request exceeded the status GET contract")
				http.Error(w, "unexpected request", http.StatusUnauthorized)
				return
			}
			calls.Add(1)
			if fail != nil && fail.Load() {
				http.Error(w, "temporarily unavailable", http.StatusServiceUnavailable)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"node_id":%q,"ready":true,"applied_revision":%q,"applied_digest":%q}`, id, rev, digestA)
		}))
	}
	a, b := serve("node-a", collectorKeyA, "rev-7", &aCalls, nil), serve("node-b", collectorKeyB, "rev-6", &bCalls, &failB)
	defer a.Close()
	defer b.Close()
	config := []CollectorNode{
		{NodeID: "node-a", AdminAddr: localAddr(a), BearerKey: collectorKeyA, DesiredRevision: ptr("rev-7"), DesiredDigest: ptr(digestA)},
		{NodeID: "node-b", AdminAddr: localAddr(b), BearerKey: collectorKeyB, DesiredRevision: ptr("rev-7"), DesiredDigest: ptr(digestA)},
	}
	c, err := NewCollector(config, 20*time.Millisecond, 300*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	*config[0].DesiredRevision = "caller-mutated"
	*config[0].DesiredDigest = digestB
	rows, err := c.Snapshots(context.Background())
	if err != nil || len(rows) != 2 || *rows[0].DesiredRevision != "rev-7" || rows[0].LastStatus != nil || rows[0].Offline || aCalls.Load() != 0 || bCalls.Load() != 0 {
		t.Fatalf("before polling: %v, %v", rows, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- c.Run(ctx) }()
	defer stopCollector(t, cancel, done)
	rows = waitSnapshots(t, c, func(rows []NodeSnapshot) bool {
		return rows[0].LastStatus != nil && rows[1].LastStatus != nil
	})
	if err := c.Run(ctx); err == nil {
		t.Fatal("a second Run overlapped the first")
	}
	*rows[0].DesiredRevision = "caller-mutated"
	*rows[0].LastStatus.AppliedRevision = "caller-mutated"
	*rows[0].LastStatus.AppliedDigest = digestB
	*rows[1].DesiredDigest = digestB
	dash := httptest.NewServer(NewDashboard(c, time.Minute))
	defer dash.Close()
	checkView := func(wantB string) {
		t.Helper()
		code, html, _ := request(t, dash.Client(), http.MethodGet, dash.URL)
		if code != 200 || !strings.Contains(html, "node-a</th><td>Ready</td><td>rev-7</td><td>rev-7</td><td>Matched</td>") || !strings.Contains(html, wantB) {
			t.Fatalf("unexpected HTTP dashboard: %d %s", code, html)
		}
		for _, forbidden := range []string{localAddr(a), localAddr(b), collectorKeyA, collectorKeyB, "caller-mutated"} {
			if strings.Contains(html, forbidden) {
				t.Fatal("dashboard exposed private configuration or mutable snapshot")
			}
		}
	}
	checkView("node-b</th><td>Ready</td><td>rev-7</td><td>rev-6</td><td>Drift</td>")
	failB.Store(true)
	waitSnapshots(t, c, func(rows []NodeSnapshot) bool {
		return rows[1].Offline && !rows[0].Offline && aCalls.Load() >= 2
	})
	checkView("node-b</th><td>Offline</td><td>rev-7</td><td>rev-6</td><td>Unverified</td>")
	failB.Store(false)
	waitSnapshots(t, c, func(rows []NodeSnapshot) bool { return !rows[1].Offline && rows[1].LastStatus != nil })
	checkView("node-b</th><td>Ready</td><td>rev-7</td><td>rev-6</td><td>Drift</td>")
}

func TestCollectorRejectsInvalidStatusAndRedirect(t *testing.T) {
	var redirects atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		redirects.Add(1)
	}))
	defer target.Close()
	for _, tc := range []struct {
		name, body, contentType string
		redirect, valid         bool
	}{
		{"known-empty-revision", `{"node_id":"node-a","ready":false,"applied_revision":null}`, "application/json; charset=utf-8", false, true},
		{"unknown-field", `{"extra":9,"node_id":"node-a","ready":false,"applied_revision":null}`, "application/json", false, true},
		{"explicit-null-digest", `{"node_id":"node-a","ready":false,"applied_revision":null,"applied_digest":null}`, "application/json", false, true},
		{"orphan-digest", `{"node_id":"node-a","ready":false,"applied_revision":null,"applied_digest":"` + digestA + `"}`, "application/json", false, false},
		{"non-string-digest", `{"node_id":"node-a","ready":false,"applied_revision":"r","applied_digest":42}`, "application/json", false, false},
		{"duplicate-digest", `{"node_id":"node-a","ready":false,"applied_revision":"r","applied_digest":null,"applied_digest":"` + digestA + `"}`, "application/json", false, false},
		{"uppercase-digest", `{"node_id":"node-a","ready":false,"applied_revision":"r","applied_digest":"` + strings.ToUpper(digestA) + `"}`, "application/json", false, false},
		{"wrong-node", `{"node_id":"node-b","ready":true,"applied_revision":null}`, "application/json", false, false},
		{"missing-ready", `{"node_id":"node-a","applied_revision":null}`, "application/json", false, false},
		{"wrong-ready-type", `{"node_id":"node-a","ready":"false","applied_revision":null}`, "application/json", false, false},
		{"null-ready", `{"node_id":"node-a","ready":null,"applied_revision":null}`, "application/json", false, false},
		{"missing-revision", `{"node_id":"node-a","ready":true}`, "application/json", false, false},
		{"unsafe-revision", `{"node_id":"node-a","ready":true,"applied_revision":"https://example.invalid"}`, "application/json", false, false},
		{"sensitive-revision", `{"node_id":"node-a","ready":true,"applied_revision":"` + collectorKeyA + `"}`, "application/json", false, false},
		{"sensitive-revision-with-affixes", `{"node_id":"node-a","ready":true,"applied_revision":"rev-` + collectorKeyA + `-draft"}`, "application/json", false, false},
		{"duplicate", `{"node_id":"node-a","ready":false,"ready":true,"applied_revision":null}`, "application/json", false, false},
		{"non-json", `{"node_id":"node-a","ready":false,"applied_revision":null}`, "text/plain", false, false},
		{"trailing-content", `{"node_id":"node-a","ready":true,"applied_revision":null} false`, "application/json", false, false},
		{"overlong-body", strings.Repeat(" ", statusLimit) + "{}\n", "application/json", false, false},
		{"redirect", "", "", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.redirect {
					http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
					return
				}
				w.Header().Set("Content-Type", tc.contentType)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			c, err := NewCollector([]CollectorNode{{NodeID: "node-a", AdminAddr: localAddr(srv), BearerKey: collectorKeyA}}, time.Minute, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			go func() { done <- c.Run(ctx) }()
			defer stopCollector(t, cancel, done)
			rows := waitSnapshots(t, c, func(rows []NodeSnapshot) bool { return rows[0].Offline || rows[0].LastStatus != nil })
			n := rows[0]
			if tc.valid {
				if n.Offline || n.LastStatus == nil || n.LastSeen.IsZero() || n.LastStatus.Ready || n.LastStatus.AppliedRevision != nil {
					t.Fatal("valid null/not-ready status was not retained")
				}
			} else if !n.Offline || n.LastStatus != nil || !n.LastSeen.IsZero() {
				t.Fatal("invalid status incorrectly counted as an observation")
			}
			if redirects.Load() != 0 {
				t.Fatal("redirect followed")
			}
		})
	}
}

func TestCollectorCancellationDoesNotPublishFailedPoll(t *testing.T) {
	started := make(chan struct{}, 1)
	blocked := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started <- struct{}{}
		<-r.Context().Done()
	}))
	defer blocked.Close()
	healthy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"node_id":"good","ready":true,"applied_revision":"1"}`))
	}))
	defer healthy.Close()
	config := []CollectorNode{
		{NodeID: "blocked", AdminAddr: localAddr(blocked), BearerKey: collectorKeyA},
		{NodeID: "good", AdminAddr: localAddr(healthy), BearerKey: collectorKeyB},
	}
	c, err := NewCollector(config, time.Second, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- c.Run(ctx) }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("first poll did not begin")
	}
	waitSnapshots(t, c, func(rows []NodeSnapshot) bool { return rows[1].LastStatus != nil })
	stopCollector(t, cancel, done)
	rows, err := c.Snapshots(context.Background())
	if err != nil || rows[0].Offline || rows[0].LastStatus != nil {
		t.Fatal("canceled poll published a failure")
	}
}

func TestCollectorInventoryValidation(t *testing.T) {
	for _, addr := range []string{
		"localhost:9200", "http://127.0.0.1:9200", "127.0.0.1:0", "127.0.0.1:9200/path", "[::1%lo]:9200", "0.0.0.0:9200", "192.0.2.1:9200",
	} {
		if _, err := NewCollector([]CollectorNode{{NodeID: "node-a", AdminAddr: addr, BearerKey: collectorKeyA}}, time.Second, time.Second); err == nil || strings.Contains(err.Error(), addr) {
			t.Fatalf("nonlocal or unsafe address accepted or echoed")
		}
	}
	one := CollectorNode{NodeID: "node-a", AdminAddr: "127.0.0.1:9200", BearerKey: collectorKeyA}
	for _, two := range []CollectorNode{
		{NodeID: one.NodeID, AdminAddr: "127.0.0.1:9201", BearerKey: collectorKeyB},
		{NodeID: "node-b", AdminAddr: one.AdminAddr, BearerKey: collectorKeyB},
		{NodeID: collectorKeyA, AdminAddr: "127.0.0.1:9201", BearerKey: collectorKeyB},
		{NodeID: "node-b", AdminAddr: "127.0.0.1:9201", BearerKey: collectorKeyB, DesiredRevision: ptr(collectorKeyA)},
		{NodeID: "node-" + collectorKeyA + "-east", AdminAddr: "127.0.0.1:9201", BearerKey: collectorKeyB},
		{NodeID: "node-b", AdminAddr: "127.0.0.1:9201", BearerKey: collectorKeyB, DesiredRevision: ptr("rev-" + collectorKeyA + "-draft")},
	} {
		if _, err := NewCollector([]CollectorNode{one, two}, time.Second, time.Second); err == nil || strings.Contains(err.Error(), collectorKeyA) {
			t.Fatal("unsafe registration accepted or echoed")
		}
	}
}

func TestCollectorHexCredentialCannotBeDigest(t *testing.T) {
	key := digestA[:32]
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"node_id":"node-a","ready":false,"applied_revision":"r","applied_digest":%q}`, digestA)
	}))
	defer srv.Close()
	node := CollectorNode{NodeID: "node-a", AdminAddr: localAddr(srv), BearerKey: key, DesiredRevision: ptr("r")}
	c, err := NewCollector([]CollectorNode{node}, time.Minute, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if status, err := c.poll(context.Background(), c.nodes[0], node.NodeID); err == nil || strings.Contains(err.Error(), key) || status != nil {
		t.Fatal("credential-bearing status was accepted or echoed")
	}
	for _, value := range []string{digestA, "", "a", strings.ToUpper(digestB)} {
		node.DesiredDigest = ptr(value)
		if _, err := NewCollector([]CollectorNode{node}, time.Minute, time.Second); err == nil || strings.Contains(err.Error(), key) {
			t.Fatal("credential-bearing or malformed desired digest was accepted or echoed")
		}
	}
	node.DesiredDigest, node.DesiredRevision = ptr(digestB), nil
	if _, err := NewCollector([]CollectorNode{node}, time.Minute, time.Second); err == nil {
		t.Fatal("orphan desired digest was accepted")
	}
}
