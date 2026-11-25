package nodeconfig_test

import (
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/codeasier/go-router/internal/catalog"
	"github.com/codeasier/go-router/internal/nodeconfig"
)

func configFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "synthetic-config-marker.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

const emptyConfig = `{"schema_version":1,"revision":"r1","providers":[]}`

const sample = `{
  "schema_version":1, "revision":"Pilot_1-r2.4", "providers":[
    {"id":"west","transport":"chat","aliases":["w"],"category":"fixture","display_name":"模型", "models":[
      {"id":"vendor/model","aliases":["fast"],"transport":"responses","enabled":true},
      {"id":"disabled","aliases":["retired"],"enabled":false}
    ]},
    {"id":"muted","transport":"messages","enabled":false,"aliases":["m"],"models":[{"id":"hidden"}]},
    {"id":"east","transport":"chat","enabled":true,"models":[{"id":"base"}]}
  ]
}`

func TestLoadProjectsOnlyEnabledModelsAndAliases(t *testing.T) {
	cfg, err := nodeconfig.Load(configFile(t, sample))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Revision() != "Pilot_1-r2.4" || cfg.Digest() != sha256.Sum256([]byte(sample)) {
		t.Fatal("revision and byte digest must match source")
	}
	reg := cfg.Catalog()
	list := reg.List()
	if len(list) != 2 || list[0].ID != "east/base" || list[0].Transport != "chat" || list[1].ID != "west/vendor/model" {
		t.Fatalf("active catalog = %+v", list)
	}
	if model, err := reg.Lookup("w/fast"); err != nil || model != list[1] || model.Transport != "responses" {
		t.Fatalf("scoped alias/transport: %+v, %v", model, err)
	}
	if p, err := reg.LookupProvider("w"); err != nil || p.DisplayName != "模型" || p.Category != "fixture" {
		t.Fatalf("provider metadata = %+v, %v", p, err)
	}

	for _, disabled := range []string{"w/retired", "west/disabled", "muted/hidden", "m/hidden"} {
		_, err := reg.Lookup(disabled)
		want := catalog.ErrUnknownModel
		if strings.HasPrefix(disabled, "m") {
			want = catalog.ErrUnknownProvider
		}
		if !errors.Is(err, want) {
			t.Fatalf("disabled %s: %v", disabled, err)
		}
	}

	// Replacing a caller-owned Registry value must not replace the snapshot's.
	*reg = catalog.Registry{}
	list[0].ID = "changed"
	if got := cfg.Catalog().List(); len(got) != 2 || got[0].ID != "east/base" {
		t.Fatalf("snapshot was mutated by its caller: %+v", got)
	}
}

func TestByteIdentityDoesNotDependOnRevisionAlone(t *testing.T) {
	path := configFile(t, emptyConfig)
	a, err := nodeconfig.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	unchanged, err := nodeconfig.Load(path)
	if err != nil || unchanged.Digest() != a.Digest() {
		t.Fatal("identical file bytes should have identical digest")
	}
	modified := emptyConfig + "\n"
	if err := os.WriteFile(path, []byte(modified), 0o600); err != nil {
		t.Fatal(err)
	}
	b, err := nodeconfig.Load(path)
	if err != nil || b.Revision() != a.Revision() || b.Digest() == a.Digest() {
		t.Fatal("edited bytes with the same label must have a different digest")
	}
	if b.Digest() != sha256.Sum256([]byte(modified)) {
		t.Fatal("digest must include trailing whitespace")
	}
}

func TestBoundaryFileSize(t *testing.T) {
	const maxBytes = 1 << 20
	exact := emptyConfig + strings.Repeat(" ", maxBytes-len(emptyConfig))
	cfg, err := nodeconfig.Load(configFile(t, exact))
	if err != nil || cfg == nil || cfg.Catalog().List() == nil || len(cfg.Catalog().List()) != 0 {
		t.Fatalf("valid empty catalog at size limit: %v", err)
	}
	if cfg, err := nodeconfig.Load(configFile(t, exact+" ")); cfg != nil || !errors.Is(err, nodeconfig.ErrSize) {
		t.Fatalf("oversize file accepted: %v", err)
	}
}

func TestExplicitRegularPathOnly(t *testing.T) {
	validDir := t.TempDir()
	for _, tc := range []struct {
		path string
		want error
	}{
		{"", nodeconfig.ErrPathRequired},
		{validDir, nodeconfig.ErrRead},
		{filepath.Join(validDir, "synthetic-config-marker.json"), nodeconfig.ErrRead},
		{"https://example.invalid/synthetic-config-marker.json", nodeconfig.ErrRead},
	} {
		if cfg, err := nodeconfig.Load(tc.path); cfg != nil || !errors.Is(err, tc.want) || strings.Contains(err.Error(), "synthetic-config-marker") {
			t.Fatalf("Load returned wrong error category, a result, or path details: %v", err)
		}
	}
}

func TestInvalidSnapshotIsRedactedAndAtomic(t *testing.T) {
	for _, tc := range []struct {
		name string
		src  string
		want error
	}{
		{"empty", "", nodeconfig.ErrInvalid},
		{"bad UTF-8", strings.ReplaceAll(emptyConfig, "r1", "r\xff1"), nodeconfig.ErrInvalid},
		{"trailing JSON", emptyConfig + " {}", nodeconfig.ErrInvalid},
		{"not an object", "[]", nodeconfig.ErrInvalid},
		{"missing version", `{"revision":"r1","providers":[]}`, nodeconfig.ErrInvalid},
		{"missing revision", `{"schema_version":1,"providers":[]}`, nodeconfig.ErrInvalid},
		{"missing providers", `{"schema_version":1,"revision":"r1"}`, nodeconfig.ErrInvalid},
		{"wrong version", strings.Replace(emptyConfig, ":1", ":2", 1), nodeconfig.ErrVersion},
		{"float version", strings.Replace(emptyConfig, ":1", ":1.0", 1), nodeconfig.ErrInvalid},
		{"string version", strings.Replace(emptyConfig, ":1", `:"1"`, 1), nodeconfig.ErrInvalid},
		{"empty revision", strings.Replace(emptyConfig, "r1", "", 1), nodeconfig.ErrRevision},
		{"revision not display-safe", strings.Replace(emptyConfig, "r1", "bad/revision", 1), nodeconfig.ErrRevision},
		{"revision too long", strings.Replace(emptyConfig, "r1", strings.Repeat("x", 129), 1), nodeconfig.ErrRevision},
		{"null revision", strings.Replace(emptyConfig, `"r1"`, "null", 1), nodeconfig.ErrInvalid},
		{"null providers", strings.Replace(emptyConfig, "[]", "null", 1), nodeconfig.ErrInvalid},
		{"null provider", strings.Replace(emptyConfig, "[]", "[null]", 1), nodeconfig.ErrInvalid},
		{"case-folded key", strings.Replace(emptyConfig, "revision", "Revision", 1), nodeconfig.ErrInvalid},
		{"duplicate equivalent key", strings.Replace(emptyConfig, `"revision"`, `"revision":"r0","revis\u0069on"`, 1), nodeconfig.ErrInvalid},
		{"unknown key", strings.Replace(emptyConfig, `"providers"`, `"synthetic-unknown-marker":"synthetic-value-marker","providers"`, 1), nodeconfig.ErrInvalid},
		{"credentials not schema", strings.Replace(sample, `"id":"west"`, `"id":"west","api_key":"synthetic-value-marker"`, 1), nodeconfig.ErrInvalid},
		{"endpoints not schema", strings.Replace(sample, `"id":"west"`, `"id":"west","url":"https://example.invalid"`, 1), nodeconfig.ErrInvalid},
		{"account not schema", strings.Replace(sample, `"id":"west"`, `"id":"west","account":"synthetic-value-marker"`, 1), nodeconfig.ErrInvalid},
		{"nested duplicate", strings.Replace(sample, `"id":"vendor/model"`, `"id":"vendor/model","id":"else"`, 1), nodeconfig.ErrInvalid},
		{"nested exact key", strings.Replace(sample, `"id":"vendor/model"`, `"ID":"vendor/model"`, 1), nodeconfig.ErrInvalid},
		{"null models", strings.Replace(sample, `[{"id":"hidden"}]`, "null", 1), nodeconfig.ErrInvalid},
		{"null enabled", strings.Replace(sample, `"enabled":false`, `"enabled":null`, 1), nodeconfig.ErrInvalid},
		{"boolean type", strings.Replace(sample, `"enabled":false`, `"enabled":"false"`, 1), nodeconfig.ErrInvalid},
		{"control metadata", strings.Replace(sample, `"模型"`, `"demo\u0000"`, 1), nodeconfig.ErrInvalid},
		{"unpaired surrogate", strings.Replace(sample, `"模型"`, `"demo\uD800"`, 1), nodeconfig.ErrInvalid},
		{"missing transport", strings.Replace(sample, `"transport":"chat",`, "", 1), nodeconfig.ErrInvalid},
		{"empty required transport", strings.Replace(sample, `"transport":"chat"`, `"transport":""`, 1), nodeconfig.ErrCatalog},
		{"ambiguous disabled provider", strings.Replace(sample, `"aliases":["m"]`, `"aliases":["w"]`, 1), nodeconfig.ErrCatalog},
		{"ambiguous disabled model", strings.Replace(sample, `"aliases":["retired"]`, `"aliases":["fast"]`, 1), nodeconfig.ErrCatalog},
		{"malformed disabled model", strings.Replace(sample, `"id":"disabled"`, `"id":"invalid/id/"`, 1), nodeconfig.ErrCatalog},
		{"invalid alias element", strings.Replace(sample, `"aliases":["fast"]`, `"aliases":[null]`, 1), nodeconfig.ErrCatalog},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := nodeconfig.Load(configFile(t, tc.src))
			if cfg != nil || !errors.Is(err, tc.want) {
				t.Fatalf("want no snapshot and %v, got snapshot=%v error=%v", tc.want, cfg != nil, err)
			}
			if strings.Contains(err.Error(), "synthetic-") {
				t.Fatal("input or path appeared in error")
			}
		})
	}
}

func TestLongestDashboardRevision(t *testing.T) {
	id := "A" + strings.Repeat(".-_", 42) + "Z"
	cfg, err := nodeconfig.Load(configFile(t, strings.Replace(emptyConfig, "r1", id, 1)))
	if err != nil || cfg.Revision() != id {
		t.Fatalf("a valid 128-byte Dashboard revision was rejected: %v", err)
	}
}

func TestDisabledProviderWithNoActivePeers(t *testing.T) {
	cfg, err := nodeconfig.Load(configFile(t, `{"schema_version":1,"revision":"r4","providers":[
		{"id":"muted","transport":"chat","enabled":false,"models":[{"id":"hidden"}]}
	]}`))
	if err != nil || cfg == nil || len(cfg.Catalog().List()) != 0 {
		t.Fatalf("disabled-only config: %v", err)
	}
	if _, err := cfg.Catalog().LookupProvider("muted"); !errors.Is(err, catalog.ErrUnknownProvider) {
		t.Fatalf("disabled provider exposed: %v", err)
	}
}
