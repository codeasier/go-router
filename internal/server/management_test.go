package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

const managementTestKey = "synthetic-only-0123456789-abcdef-0123456789"

func TestManagementStatus(t *testing.T) {
	admin, err := NewManagement("127.0.0.1:12345", "west-1", managementTestKey)
	if err != nil {
		t.Fatal(err)
	}
	handle := func(method, path string, auth ...string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, nil)
		for _, a := range auth {
			r.Header.Add("Authorization", a)
		}
		w := httptest.NewRecorder()
		admin.Handler().ServeHTTP(w, r)
		return w
	}
	const endpoint = "/admin/v1/status"
	for _, auth := range [][]string{
		nil,
		{"Bearer "},
		{"Basic " + managementTestKey},
		{"Bearer " + strings.Repeat("x", len(managementTestKey))},
		{"Bearer " + managementTestKey, "Bearer " + managementTestKey},
		{"Bearer " + managementTestKey + ",Bearer " + managementTestKey},
	} {
		rec := handle("GET", endpoint, auth...)
		if rec.Code != 401 || rec.Header().Get("WWW-Authenticate") != "Bearer" ||
			rec.Header().Get("Cache-Control") != "no-store" ||
			strings.Contains(rec.Body.String(), managementTestKey) || strings.Contains(rec.Body.String(), "west-1") {
			t.Errorf("unauthorized response: HTTP %d, headers %v", rec.Code, rec.Header())
		}
	}
	if got := handle("GET", endpoint+"?access_token="+managementTestKey).Code; got != 401 {
		t.Errorf("URL credential status = %d, want 401", got)
	}
	auth := "bearer " + managementTestKey
	rec := handle("GET", endpoint, auth)
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "application/json" || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("status response: HTTP %d, headers %v", rec.Code, rec.Header())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"node_id": "west-1", "ready": false, "applied_revision": nil, "applied_digest": nil}
	if !reflect.DeepEqual(body, want) {
		t.Errorf("fields = %v, want %v", body, want)
	}
	for _, method := range []string{"POST", "PATCH", "DELETE", "HEAD"} {
		if got := handle(method, endpoint, auth); got.Code != 405 || got.Header().Get("Allow") != "GET" {
			t.Errorf("%s: HTTP %d, Allow %q", method, got.Code, got.Header().Get("Allow"))
		}
	}
	for _, path := range []string{"/v1/models", "/healthz", "/readyz"} {
		if got := handle("GET", path, auth).Code; got != 404 {
			t.Errorf("management listener %s = %d, want 404", path, got)
		}
	}
}

func TestManagementConfiguration(t *testing.T) {
	for _, addr := range []string{"", ":9401", "0.0.0.0:9401", "[::]:9401", "192.0.2.1:9401", "localhost:9401", "127.0.0.1:0", "[::1%lo]:9401"} {
		if _, err := NewManagement(addr, "west-1", managementTestKey); err == nil {
			t.Errorf("accepted invalid management address %q", addr)
		}
	}
	for _, id := range []string{"", "west/1", "_west", strings.Repeat("a", 65), "key\n"} {
		if _, err := NewManagement("[::1]:9401", id, managementTestKey); err == nil {
			t.Errorf("accepted invalid node ID")
		}
	}
	for _, key := range []string{"", "short-key", strings.Repeat("a", 257), strings.Repeat("a", 32) + "\n", strings.Repeat("a", 32) + " secret"} {
		if _, err := NewManagement("[::1]:9401", "west-1", key); err == nil || (key != "" && strings.Contains(err.Error(), key)) {
			t.Errorf("accepted or echoed invalid management key")
		}
	}
	if _, err := NewManagement("[::1]:9401", "WEST.1_a", managementTestKey); err != nil {
		t.Fatal(err)
	}
}

func TestDataPlaneReadinessAndIsolation(t *testing.T) {
	rec := serve(t, http.MethodGet, "/readyz")
	if rec.Code != http.StatusServiceUnavailable || strings.TrimSpace(rec.Body.String()) != `{"ready":false}` {
		t.Errorf("empty data-plane readiness: HTTP %d %s", rec.Code, rec.Body.String())
	}
	req := httptest.NewRequest(http.MethodGet, "/admin/v1/status", nil)
	req.Header.Set("Authorization", "Bearer "+managementTestKey)
	rec = httptest.NewRecorder()
	New(":0").Handler().ServeHTTP(rec, req)
	if rec.Code != 404 {
		t.Errorf("inference listener serves management: HTTP %d", rec.Code)
	}
}

func TestAppliedIdentityOnlyInAuthenticatedStatus(t *testing.T) {
	calls := 0
	digest := strings.Repeat("a", 64)
	admin, err := NewManagementWithIdentity("127.0.0.1:12345", "west-1", managementTestKey, func() (string, string) {
		calls++
		return "revision-1", digest
	})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/admin/v1/status", nil)
	rec := httptest.NewRecorder()
	admin.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized || calls != 0 || strings.Contains(rec.Body.String(), "revision-1") {
		t.Fatal("unauthenticated request accessed active configuration")
	}
	req.Header.Set("Authorization", "Bearer "+managementTestKey)
	rec = httptest.NewRecorder()
	admin.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || calls != 1 || strings.TrimSpace(rec.Body.String()) != `{"node_id":"west-1","ready":false,"applied_revision":"revision-1","applied_digest":"`+digest+`"}` {
		t.Fatal("authenticated status did not publish applied metadata without asserting readiness")
	}
}
