// Package catalog validates a node's provider and model metadata before use.
// Names are exact and case-sensitive. Each component starts with an ASCII
// letter or digit, then allows ASCII letters, digits, and "-_.:@". Only
// model names can contain multiple slash-separated components.
// A Registry contains no credentials or executable transports. Construct a new
// registry to validate a replacement snapshot; never edit a live registry.
package catalog

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

// ProviderSpec defines a case-sensitive provider namespace and its default
// transport identifier. Aliases share the same namespace as provider IDs.
type ProviderSpec struct {
	ID          string
	Aliases     []string
	DisplayName string
	Category    string
	Transport   string
	Models      []ModelSpec
}

// ModelSpec names a model within a provider. An ID (or alias) may itself
// contain slashes, for example "publisher/model". Empty Transport inherits
// the provider's default; a non-empty value explicitly overrides it.
type ModelSpec struct {
	ID          string
	Aliases     []string
	DisplayName string
	Transport   string
}

// Provider is canonical metadata, irrespective of the alias used to find it.
type Provider struct {
	ID          string
	DisplayName string
	Category    string
	Transport   string
}

// Model contains the canonical qualified ID, upstream model name, and resolved
// transport identifier. Transport is metadata, not an executable capability.
type Model struct {
	ID          string // provider/model
	ProviderID  string
	Name        string // model without the provider prefix
	DisplayName string
	Transport   string
}

type namespace struct {
	provider Provider
	models   map[string]Model
}

// Registry is a read-only snapshot, safe for concurrent lookups. The zero
// value is an empty registry.
type Registry struct {
	namespaces map[string]*namespace
	models     []Model // canonical entries only, ordered by qualified ID
}

var (
	ErrUnknownProvider = errors.New("unknown catalog provider")
	ErrUnknownModel    = errors.New("unknown catalog model")
	ErrInvalidModelID  = errors.New("expected a qualified provider/model identifier")
)

// validName accepts slash-separated ASCII identifiers for models; provider
// and transport names are single identifiers. Do not trim, path-clean or fold
// names: these keys identify a specific model and ambiguity must fail closed.
func validName(s string, allowSlash bool) bool {
	start := true
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
			start = false
		case c == '/' && allowSlash && !start:
			start = true
		case (c == '-' || c == '_' || c == '.' || c == ':' || c == '@') && !start:
		default:
			return false
		}
	}
	return !start
}

func bind[T any](names map[string]T, id string, aliases []string, value T, allowSlash bool) error {
	add := func(name string) error {
		if !validName(name, allowSlash) {
			return errors.New("invalid identifier")
		}
		if _, exists := names[name]; exists {
			return errors.New("duplicate identifier")
		}
		names[name] = value
		return nil
	}
	if err := add(id); err != nil {
		return fmt.Errorf("ID: %w", err)
	}
	for i, alias := range aliases {
		if err := add(alias); err != nil {
			return fmt.Errorf("alias[%d]: %w", i, err)
		}
	}
	return nil
}

// New validates the entire snapshot before returning any registry. Provider
// names must be unique globally and model names unique within their provider,
// whether the name is canonical or an alias. Empty input is a valid catalog.
func New(providers []ProviderSpec) (*Registry, error) {
	r := &Registry{namespaces: make(map[string]*namespace)}
	for i, p := range providers {
		if !validName(p.Transport, false) {
			return nil, fmt.Errorf("provider[%d]: missing or invalid transport", i)
		}
		ns := &namespace{
			provider: Provider{ID: p.ID, DisplayName: p.DisplayName, Category: p.Category, Transport: p.Transport},
			models:   make(map[string]Model),
		}
		if err := bind(r.namespaces, p.ID, p.Aliases, ns, false); err != nil {
			return nil, fmt.Errorf("provider[%d]: %w", i, err)
		}
		for j, m := range p.Models {
			transport := m.Transport
			if transport == "" {
				transport = p.Transport
			} else if !validName(transport, false) {
				return nil, fmt.Errorf("provider[%d] model[%d]: invalid transport", i, j)
			}
			model := Model{
				ID: p.ID + "/" + m.ID, ProviderID: p.ID, Name: m.ID,
				DisplayName: m.DisplayName, Transport: transport,
			}
			if err := bind(ns.models, m.ID, m.Aliases, model, true); err != nil {
				return nil, fmt.Errorf("provider[%d] model[%d]: %w", i, j, err)
			}
			r.models = append(r.models, model)
		}
	}
	slices.SortFunc(r.models, func(a, b Model) int { return strings.Compare(a.ID, b.ID) })
	return r, nil
}

// LookupProvider resolves a provider ID or alias and returns only canonical
// metadata. Nothing returned by Registry shares mutable state with it.
func (r *Registry) LookupProvider(name string) (Provider, error) {
	ns, ok := r.namespaces[name]
	if !ok {
		return Provider{}, ErrUnknownProvider
	}
	return ns.provider, nil
}

// Lookup resolves provider/model using the first slash as separator. Either
// name may be an explicitly configured alias. There is no fuzzy match,
// unqualified search, fallback or cross-provider alias traversal.
func (r *Registry) Lookup(id string) (Model, error) {
	provider, model, found := strings.Cut(id, "/")
	if !found || !validName(provider, false) || !validName(model, true) {
		return Model{}, ErrInvalidModelID
	}
	ns, ok := r.namespaces[provider]
	if !ok {
		return Model{}, ErrUnknownProvider
	}
	m, ok := ns.models[model]
	if !ok {
		return Model{}, ErrUnknownModel
	}
	return m, nil
}

// List returns one canonical entry per model, sorted by qualified ID. The
// slice belongs to the caller and is non-nil even when the catalog is empty.
func (r *Registry) List() []Model {
	out := make([]Model, len(r.models))
	copy(out, r.models)
	return out
}
