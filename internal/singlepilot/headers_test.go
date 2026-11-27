package singlepilot

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/codeasier/go-router/internal/attemptlog"
)

func TestParseGenericChatHeaderSnapshot(t *testing.T) {
	got, err := parseCustomHeaders(`{"x-tenant":"wrong","X-tenant":" right ","X-Edge.Tag":"abc  def","X-Empty":"","User-Agent":"dedicated"} `)
	want := map[string]string{"X-Tenant": "right", "X-Edge.tag": "abc  def", "X-Empty": "", "User-Agent": "dedicated"}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("did not preserve case-insensitive precedence and opaque values: %v, %v", got, err)
	}
	for _, raw := range []string{`{}`, `{"X-Name":"$OTHER"}`, `{"X-Name":"ok"}` + strings.Repeat(" ", maxHeadersConfigBytes-len(`{"X-Name":"ok"}`))} {
		if _, err := parseCustomHeaders(raw); err != nil {
			t.Fatal(err)
		}
	}
}

func TestGenericChatHeadersFailClosed(t *testing.T) {
	raws := []string{"", " ", "[]", "null", "false", `{"X-Name":"ok",}`, `{}{}`, `{"X-Name":"ok"}false`,
		`{"X-Name":"first","X-Name":"second"}`, `{"X-N\u0061me":"x","X-Name":"y"}`,
		`{" X-Name":"v"}`, `{"X-Name\n":"v"}`, `{"X:Name":"v"}`, `{"X-Name":["v"]}`,
		`{"X-Name":{"inner":true}}`, `{"X-Name":null}`, `{"X-Name":10}`, `{"X-Name":true}`,
		`{"X-Name":"ok\r\nX-Injection: fixture-secret"}`, `{"X-Name":"\u0000"}`, `{"X-Name":"\t"}`,
		`{"X-Name":"\ud800"}`, `{"X-Name":"中"}`, `{"X-Name":"${SECRET}"}`, `{"X-Name":"${"}`, `{"user-agent":" "}`,
		`{"X-Name":"` + strings.Repeat("a", maxCustomHeaderValue+1) + `"}`,
		`{"` + strings.Repeat("A", maxCustomHeaderName+1) + `":"v"}`,
		`{}` + strings.Repeat(" ", maxHeadersConfigBytes-1),
	}
	oversize := make(map[string]string)
	for i := range maxCustomHeaders + 1 {
		oversize[fmt.Sprintf("X-Field-%d", i)] = "value"
	}
	var b []byte
	b, _ = json.Marshal(oversize)
	raws = append(raws, string(b))
	oversize = make(map[string]string)
	for i := range 4 {
		oversize[fmt.Sprintf("X-Field-%d", i)] = strings.Repeat("a", maxCustomHeaderValue)
	}
	b, _ = json.Marshal(oversize)
	raws = append(raws, string(b))
	for _, k := range []string{"Authorization", "x-API-kEy", "api-key", "X-Auth-Token", "Accept", "AcCept-Encoding", "Content-Type", "hOSt",
		"Content-Length", "TRANSFER-encoding", "Connection", "Keep-Alive", "Expect", "Te", "Trailer", "UpGrade", "HTTP2-Settings",
		"Proxy-Authorization", "proxy-authenticate", "pRoXy-connection", "Proxy-Something", "Cookie", "forwardED", "x-FoRwArDeD-Host", "X-Http-Method-Override"} {
		raws = append(raws, fmt.Sprintf(`{"%s":"fixture-secret"}`, k))
	}
	for i, raw := range raws {
		h, err := NewConfigured("custom", []string{"a"}, "https://provider.invalid/v1", "fixture-key",
			journalFunc(func(attemptlog.Intent) error { t.Fatal("configuration must not record intent"); return nil }),
			ChatOptions{HeadersJSON: &raw})
		if h != nil || !errors.Is(err, errCustomHeaders) || strings.Contains(err.Error(), "fixture-secret") {
			t.Fatalf("case %d did not fail safely", i)
		}
	}
}

func TestGenericChatHeaderOptions(t *testing.T) {
	for _, raw := range []string{`{}`, `{"X-Tenant":"dedicated","X-Empty":""}`} {
		h, err := NewConfigured("custom", []string{"a"}, "https://provider.invalid/v1", "fixture-key",
			journalFunc(func(attemptlog.Intent) error { return nil }), ChatOptions{HeadersJSON: &raw})
		if err != nil || h == nil {
			t.Fatal("valid config failed")
		}
		if fmt.Sprintf("%#v", ChatOptions{HeadersJSON: &raw}) != "generic Chat options (redacted)" {
			t.Fatal("options could log secret values")
		}
	}
	// Check exact canonicalization used for outbound header storage.
	if !validCustomHeaderName("X-A.B_`|~") || http.CanonicalHeaderKey("X-A.B_`|~") != "X-A.b_`|~" {
		t.Fatal("valid token no longer supported")
	}
}
