package openaichat

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestBaseURL(t *testing.T) {
	for base, want := range map[string]string{
		"https://provider.invalid":         "https://provider.invalid/chat/completions",
		"https://provider.invalid/v1/":     "https://provider.invalid/v1/chat/completions",
		"https://provider.invalid/api/v1":  "https://provider.invalid/api/v1/chat/completions",
		"https://router.invalid:8443/team": "https://router.invalid:8443/team/chat/completions",
		"https://[::1]:8443/v1":            "https://[::1]:8443/v1/chat/completions",
	} {
		a, err := New(base)
		if err != nil || a.endpoint != want {
			t.Errorf("base %q failed: %v", base, err)
		}
	}
	for _, bad := range []string{
		"", "/v1", "http://provider.invalid", "https:provider.invalid/v1",
		"https://user:secret@provider.invalid/v1", "https://provider.invalid/v1?secret=x",
		"https://provider.invalid/v1?", "https://provider.invalid/#", "https://provider.invalid:/v1",
		"https://provider.invalid:0/v1", "https://provider.invalid:65536/v1",
		"https://[not-an-ip]/v1", "https://[::1%25eth0]/v1",
		"https://provider.invalid//", "https://provider.invalid/v1//next",
		"https://provider.invalid/../v1", "https://provider.invalid/v1%2fnext",
		"https://provider.invalid/v1/chat/completions", "https://provider.invalid/v1\\other",
	} {
		if _, err := New(bad); err != ErrEndpoint {
			t.Errorf("invalid base accepted: %q", bad)
		}
	}
}

func TestPreparePreservesJSONExtensionsAndSealsWire(t *testing.T) {
	a, err := New("https://provider.invalid/api/v1")
	if err != nil {
		t.Fatal(err)
	}
	raw := []byte(`{"model":"local/alias","stream":true,"messages":[{"role":"user","content":[{"type":"text","text":"synthetic prompt"},{"type":"image_url","image_url":{"url":"data:image/png;base64,AA=="}}]}],"tools":[{"type":"function","function":{"name":"probe"}}],"vendor_extra":{"sequence":9007199254740993},"stream_options":{"include_usage":true}}`)
	p, err := a.Prepare("upstream/model", raw)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(fmt.Sprintf("%+v %#v", p, p), "synthetic prompt") {
		t.Fatal("prepared request leaked through formatting")
	}
	r, err := p.NewRequest(context.Background(), "fixture-token")
	if err != nil {
		t.Fatal(err)
	}
	if r.Method != http.MethodPost || r.URL.String() != "https://provider.invalid/api/v1/chat/completions" ||
		r.Header.Get("Authorization") != "Bearer fixture-token" || r.Header.Get("Accept") != "text/event-stream" ||
		r.Header.Get("Content-Type") != "application/json" || len(r.Header) != 3 || r.GetBody != nil {
		t.Fatal("unexpected request method, target, auth, media, or replay behavior")
	}
	defer r.Body.Close()
	wire, err := io.ReadAll(r.Body)
	if err != nil || sha256.Sum256(wire) != p.BodyDigest() || r.ContentLength != int64(len(wire)) {
		t.Fatal("digest or content length differs from the sent wire body")
	}
	var got, original map[string]json.RawMessage
	if json.Unmarshal(wire, &got) != nil || json.Unmarshal(raw, &original) != nil ||
		string(got["model"]) != `"upstream/model"` || len(got) != len(original) {
		t.Fatal("unexpected payload projection")
	}
	for name, value := range original {
		if name != "model" && !bytes.Equal(got[name], value) {
			t.Errorf("non-model extension changed: %s", name)
		}
	}
	for _, token := range []string{"", "Bearer wrong", "embedded\r\nheader", strings.Repeat("x", 4097)} {
		if _, err := p.NewRequest(context.Background(), token); !errors.Is(err, ErrCredential) {
			t.Fatal("invalid credential accepted")
		}
	}
}

func TestMalformedAndBoundedRequest(t *testing.T) {
	a, err := New("https://provider.invalid/v1")
	if err != nil {
		t.Fatal(err)
	}
	for _, in := range []string{
		"null", "[]", "{}", `{"stream":null}`, `{"stream":"on"}`,
		`{"STREAM":true}`, `{"MODEL":"other"}`, `{"stream":true,"st\u0072eam":false}`,
		`{"x":1,"x":2}`, `{"stream":true}{}`, "{\"x\":\"\xff\"}",
		`{"x":"` + strings.Repeat("x", maxRequest) + `"}`,
	} {
		model := "model"
		if in == "{}" {
			model = "bad\nmodel"
		}
		if _, err := a.Prepare(model, []byte(in)); !errors.Is(err, ErrRequest) {
			t.Errorf("invalid payload accepted (size=%d): %v", len(in), err)
		}
	}
}
