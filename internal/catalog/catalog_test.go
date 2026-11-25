package catalog_test

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/codeasier/go-router/internal/catalog"
)

func sample() []catalog.ProviderSpec {
	return []catalog.ProviderSpec{
		{
			ID: "west", Aliases: []string{"w"}, DisplayName: "West lab",
			Category: "fixture", Transport: "chat",
			Models: []catalog.ModelSpec{
				{ID: "publisher/nova:1", DisplayName: "Nova", Aliases: []string{"fast", "team/stable"}, Transport: "responses"},
				{ID: "base"},
			},
		},
		{ID: "east", Transport: "messages", Models: []catalog.ModelSpec{{ID: "base", Aliases: []string{"fast"}}}},
	}
}

func TestLookupAndList(t *testing.T) {
	spec := sample()
	r, err := catalog.New(spec)
	if err != nil {
		t.Fatal(err)
	}
	want := catalog.Model{ID: "west/publisher/nova:1", ProviderID: "west", Name: "publisher/nova:1", DisplayName: "Nova", Transport: "responses"}
	for _, name := range []string{"west/publisher/nova:1", "w/publisher/nova:1", "west/fast", "w/team/stable"} {
		if got, err := r.Lookup(name); got != want || err != nil {
			t.Errorf("Lookup(%q) = %+v, %v; want %+v", name, got, err, want)
		}
	}
	if p, err := r.LookupProvider("w"); err != nil || p != (catalog.Provider{ID: "west", DisplayName: "West lab", Category: "fixture", Transport: "chat"}) {
		t.Fatalf("LookupProvider(alias) = %+v, %v", p, err)
	}
	if _, err := r.LookupProvider("nowhere"); !errors.Is(err, catalog.ErrUnknownProvider) {
		t.Fatalf("LookupProvider(unknown): %v", err)
	}
	east, err := r.Lookup("east/fast")
	if err != nil || east.ID != "east/base" || east.Transport != "messages" {
		t.Fatalf("provider scope/default transport: %+v, %v", east, err)
	}
	wantList := []catalog.Model{east, {ID: "west/base", ProviderID: "west", Name: "base", Transport: "chat"}, want}
	if got := r.List(); !slices.Equal(got, wantList) {
		t.Fatalf("List() = %+v; want %+v", got, wantList)
	}
	slices.Reverse(spec[0].Models)
	slices.Reverse(spec)
	reversed, err := catalog.New(spec)
	if err != nil || !slices.Equal(reversed.List(), wantList) {
		t.Fatalf("permuted input changed results: %v", err)
	}
}

func TestUnknownAndMalformedRequests(t *testing.T) {
	r, err := catalog.New(sample())
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		id   string
		want error
	}{
		{"WEST/base", catalog.ErrUnknownProvider}, {"east/FAST", catalog.ErrUnknownModel},
		{"east/publisher/nova:1", catalog.ErrUnknownModel}, {"base", catalog.ErrInvalidModelID},
		{"/base", catalog.ErrInvalidModelID}, {"west/", catalog.ErrInvalidModelID},
		{"west//base", catalog.ErrInvalidModelID}, {"west/../base", catalog.ErrInvalidModelID},
		{"west/bad key", catalog.ErrInvalidModelID}, {"west/base\x00", catalog.ErrInvalidModelID},
	} {
		got, err := r.Lookup(tc.id)
		if !errors.Is(err, tc.want) || got != (catalog.Model{}) {
			t.Errorf("Lookup(%q) = %+v, %v; want zero, %v", tc.id, got, err, tc.want)
		}
	}
}

func TestRegistryOwnsItsSnapshot(t *testing.T) {
	spec := sample()
	r, err := catalog.New(spec)
	if err != nil {
		t.Fatal(err)
	}
	spec[0].Aliases[0] = "changed"
	spec[0].Models[0].Aliases[0] = "changed"
	spec[0].Models[0].ID = "changed"
	spec[0] = catalog.ProviderSpec{}
	list := r.List()
	list[0].ID = "changed"
	p, _ := r.LookupProvider("w")
	p.Category = "changed"
	if got, err := r.Lookup("w/fast"); err != nil || got.ID != "west/publisher/nova:1" {
		t.Fatalf("mutating input invalidated snapshot: %+v, %v", got, err)
	}
	if next := r.List(); next[0].ID != "east/base" {
		t.Fatalf("mutating output invalidated snapshot: %+v", next)
	}
	if p, _ := r.LookupProvider("west"); p.Category != "fixture" {
		t.Fatalf("provider output mutates registry: %+v", p)
	}
}

func TestValidationIsAtomic(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func([]catalog.ProviderSpec)
		at   string
	}{
		{"missing provider", func(s []catalog.ProviderSpec) { s[0].ID = "" }, "provider[0]: ID"},
		{"invalid provider", func(s []catalog.ProviderSpec) { s[0].ID = "bad/name" }, "provider[0]: ID"},
		{"repeated provider", func(s []catalog.ProviderSpec) { s[1].ID = "west" }, "provider[1]: ID"},
		{"provider alias masks later ID", func(s []catalog.ProviderSpec) { s[0].Aliases = []string{"east"} }, "provider[1]: ID"},
		{"provider alias masks earlier ID", func(s []catalog.ProviderSpec) { s[1].Aliases = []string{"west"} }, "provider[1]: alias[0]"},
		{"repeated provider alias", func(s []catalog.ProviderSpec) { s[0].Aliases = []string{"w", "w"} }, "provider[0]: alias[1]"},
		{"provider self alias", func(s []catalog.ProviderSpec) { s[0].Aliases = []string{"west"} }, "provider[0]: alias[0]"},
		{"no transport", func(s []catalog.ProviderSpec) { s[0].Transport = "" }, "provider[0]"},
		{"invalid transport", func(s []catalog.ProviderSpec) { s[0].Transport = "x/y" }, "provider[0]"},
		{"model transport whitespace", func(s []catalog.ProviderSpec) { s[0].Models[0].Transport = " chat" }, "model[0]"},
		{"empty model", func(s []catalog.ProviderSpec) { s[0].Models[1].ID = "" }, "model[1]: ID"},
		{"trailing slash", func(s []catalog.ProviderSpec) { s[0].Models[0].ID = "a/" }, "model[0]: ID"},
		{"invalid alias", func(s []catalog.ProviderSpec) { s[0].Models[0].Aliases = []string{".."} }, "model[0]: alias[0]"},
		{"repeated model", func(s []catalog.ProviderSpec) { s[0].Models[0].ID = "base" }, "model[1]: ID"},
		{"model alias masks later ID", func(s []catalog.ProviderSpec) { s[0].Models[0].Aliases = []string{"base"} }, "model[1]: ID"},
		{"model alias masks earlier ID", func(s []catalog.ProviderSpec) { s[0].Models[1].Aliases = []string{"publisher/nova:1"} }, "model[1]: alias[0]"},
		{"colliding model aliases", func(s []catalog.ProviderSpec) { s[0].Models[1].Aliases = []string{"fast"} }, "model[1]: alias[0]"},
		{"model self alias", func(s []catalog.ProviderSpec) { s[0].Models[1].Aliases = []string{"base"} }, "model[1]: alias[0]"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := sample()
			tc.edit(s)
			if got, err := catalog.New(s); got != nil || err == nil || !strings.Contains(err.Error(), tc.at) {
				t.Errorf("New: got registry %v, err %v; want nil and index %q", got != nil, err, tc.at)
			}
		})
	}
}

func TestEmptyCatalog(t *testing.T) {
	r, err := catalog.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, empty := range []*catalog.Registry{r, {}} {
		if got := empty.List(); got == nil || len(got) != 0 {
			t.Fatalf("List() = %#v; want empty non-nil", got)
		}
		if _, err := empty.Lookup("east/base"); !errors.Is(err, catalog.ErrUnknownProvider) {
			t.Fatalf("empty Lookup() = %v", err)
		}
	}
}

func TestConcurrentReaders(t *testing.T) {
	r, err := catalog.New(sample())
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 8; i++ {
		t.Run("reader", func(t *testing.T) {
			t.Parallel()
			for j := 0; j < 32; j++ {
				if got, err := r.Lookup("w/fast"); err != nil || got.ID != "west/publisher/nova:1" {
					t.Fatalf("Lookup: %+v, %v", got, err)
				}
				models := r.List()
				if models[0].ID != "east/base" {
					t.Fatalf("shared list: %v", models)
				}
				models[0].ID = ""
			}
		})
	}
}
