// Package nodeconfig loads the pilot's versioned, metadata-only JSON snapshot.
// A caller must supply a local path; this package does not read environment
// variables or contact a controller or upstream.
package nodeconfig

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"os"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/codeasier/go-router/internal/catalog"
)

// MaxBytes bounds every metadata snapshot, independent of the source.
const MaxBytes = 1 << 20

var (
	ErrPathRequired = errors.New("node config: an explicit local file path is required")
	ErrRead         = errors.New("node config: cannot read a regular file")
	ErrSize         = errors.New("node config: file exceeds 1 MiB")
	ErrInvalid      = errors.New("node config: invalid JSON snapshot")
	ErrVersion      = errors.New("node config: unsupported schema version")
	ErrRevision     = errors.New("node config: invalid revision identifier")
	ErrCatalog      = errors.New("node config: invalid model catalog")
)

// Match the Dashboard's opaque revision identifier grammar. A revision is
// suitable for operator display; do not put credentials or URLs in it.
var revisionPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// Snapshot is a validated configuration value. It exposes no editable slices,
// maps, file path or source bytes.
type Snapshot struct {
	revision string
	digest   [sha256.Size]byte
	catalog  catalog.Registry
}

func (s *Snapshot) Revision() string { return s.revision }

// Digest is the SHA-256 identity of the exact accepted bytes, including
// whitespace. It is distinct from the revision label. Management may report
// it to authorized callers; the artifact bytes themselves remain local.
func (s *Snapshot) Digest() [sha256.Size]byte { return s.digest }

// Catalog returns a read-only registry of the enabled providers and models.
// Copy the registry value so overwriting the returned handle cannot change
// this snapshot. The registry itself never exposes mutable backing storage.
func (s *Snapshot) Catalog() *catalog.Registry {
	copy := s.catalog
	return &copy
}

// Load reads and validates a complete snapshot. All definitions, including
// disabled ones, are validated before filtering. Errors deliberately omit
// paths, field names or values from the supplied input.
func Load(path string) (*Snapshot, error) {
	if path == "" {
		return nil, ErrPathRequired
	}
	// A trusted operator supplies the path. Probe before opening so ordinary
	// misconfigurations such as a directory, device or FIFO do not block.
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return nil, ErrRead
	}
	if info.Size() > MaxBytes {
		return nil, ErrSize
	}
	f, err := openRegular(path, info)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	src, err := io.ReadAll(io.LimitReader(f, MaxBytes+1))
	if err != nil {
		return nil, ErrRead
	}
	return Parse(src)
}

// The caller's path is trusted. On Unix the open is nonblocking so even
// a race replacing the file with a pipe will not stall a reload. Validate
// the opened descriptor before reading any bytes, on all platforms.
func openRegular(path string, before os.FileInfo) (*os.File, error) {
	f, err := openConfigFile(path)
	if err != nil {
		return nil, ErrRead
	}
	after, err := f.Stat()
	if err != nil || !after.Mode().IsRegular() || !os.SameFile(before, after) {
		f.Close()
		return nil, ErrRead
	}
	return f, nil
}

// Parse validates one bounded, complete metadata snapshot without retaining src.
// The digest represents the exact bytes that were validated.
func Parse(src []byte) (*Snapshot, error) {
	if len(src) > MaxBytes {
		return nil, ErrSize
	}
	var w wireConfig
	if !utf8.Valid(src) || json.Unmarshal(src, &w) != nil {
		return nil, ErrInvalid
	}
	if w.version != 1 {
		return nil, ErrVersion
	}
	if !revisionPattern.MatchString(w.revision) {
		return nil, ErrRevision
	}

	full, active := w.providersForCatalog()
	validated, err := catalog.New(full)
	if err != nil {
		return nil, ErrCatalog
	}
	if active != nil {
		validated, err = catalog.New(active)
		if err != nil {
			return nil, ErrCatalog
		}
	}
	return &Snapshot{revision: w.revision, digest: sha256.Sum256(src), catalog: *validated}, nil
}

type wireConfig struct {
	version   int
	revision  string
	providers []wireProvider
}

func (w *wireConfig) UnmarshalJSON(src []byte) error {
	return object(src,
		field{"schema_version", &w.version, true},
		field{"revision", &w.revision, true},
		field{"providers", &w.providers, true},
	)
}

type wireProvider struct {
	spec    catalog.ProviderSpec
	enabled bool
	models  []wireModel
}

func (w *wireProvider) UnmarshalJSON(src []byte) error {
	w.enabled = true
	return object(src,
		field{"id", &w.spec.ID, true},
		field{"enabled", &w.enabled, false},
		field{"aliases", &w.spec.Aliases, false},
		field{"display_name", &w.spec.DisplayName, false},
		field{"category", &w.spec.Category, false},
		field{"transport", &w.spec.Transport, true},
		field{"models", &w.models, true},
	)
}

type wireModel struct {
	spec    catalog.ModelSpec
	enabled bool
}

func (w *wireModel) UnmarshalJSON(src []byte) error {
	w.enabled = true
	return object(src,
		field{"id", &w.spec.ID, true},
		field{"enabled", &w.enabled, false},
		field{"aliases", &w.spec.Aliases, false},
		field{"display_name", &w.spec.DisplayName, false},
		field{"transport", &w.spec.Transport, false},
	)
}

func (w wireConfig) providersForCatalog() ([]catalog.ProviderSpec, []catalog.ProviderSpec) {
	full := make([]catalog.ProviderSpec, 0, len(w.providers))
	active := make([]catalog.ProviderSpec, 0, len(w.providers))
	filtered := false
	for _, p := range w.providers {
		allModels := make([]catalog.ModelSpec, 0, len(p.models))
		activeModels := make([]catalog.ModelSpec, 0, len(p.models))
		for _, m := range p.models {
			allModels = append(allModels, m.spec)
			if m.enabled {
				activeModels = append(activeModels, m.spec)
			} else {
				filtered = true
			}
		}
		spec := p.spec
		spec.Models = allModels
		full = append(full, spec)
		if p.enabled {
			spec.Models = activeModels
			active = append(active, spec)
		} else {
			filtered = true
		}
	}
	if !filtered {
		active = nil
	}
	return full, active
}

type field struct {
	name     string
	target   any
	required bool
}

// encoding/json alone accepts duplicate keys, case-folded keys and nulls for
// non-pointer scalar fields. Read exact keys, reject duplicate decoded names
// (including escaped keys) and only then decode each value into its wire type.
func object(src []byte, fields ...field) error {
	d := json.NewDecoder(bytes.NewReader(src))
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return ErrInvalid
	}
	seen := make([]bool, len(fields))
	for d.More() {
		token, err := d.Token()
		key, ok := token.(string)
		if err != nil || !ok {
			return ErrInvalid
		}
		i := 0
		for i < len(fields) && fields[i].name != key {
			i++
		}
		if i == len(fields) || seen[i] {
			return ErrInvalid
		}
		seen[i] = true
		var value json.RawMessage
		if d.Decode(&value) != nil || bytes.Equal(value, []byte("null")) || json.Unmarshal(value, fields[i].target) != nil {
			return ErrInvalid
		}
		if value, isString := fields[i].target.(*string); isString && strings.ContainsFunc(*value, invalidTextRune) {
			return ErrInvalid
		}
	}
	if token, err = d.Token(); err != nil || token != json.Delim('}') {
		return ErrInvalid
	}
	for i, f := range fields {
		if f.required && !seen[i] {
			return ErrInvalid
		}
	}
	return nil
}

// A decoded replacement character may indicate an unpaired JSON escape.
// It and control characters are not permitted in snapshot metadata.
func invalidTextRune(r rune) bool {
	return r == utf8.RuneError || unicode.IsControl(r)
}
