package cardigann

import (
	"context"
	"strings"
	"testing"

	"github.com/buzzqw/indexarr/internal/definition"
)

func TestDerivedFromConstant(t *testing.T) {
	if DerivedFrom != "cardigann" {
		t.Errorf("DerivedFrom = %q, want cardigann", DerivedFrom)
	}
}

func TestConverterAlwaysEmitsDerivedFrom(t *testing.T) {
	fixtures := []string{
		"private-html.yml",
		"public-html.yml",
		"json-api.yml",
		"filters.yml",
		"unsupported.yml",
		"secrets.yml",
		"headers-list.yml",
		"template-controls.yml",
		"unit3d-api.yml",
		"torznab-api.yml",
		"newznab-api.yml",
	}
	for _, name := range fixtures {
		t.Run(name, func(t *testing.T) {
			result := convertFixture(t, name)
			if result.Definition.Meta.DerivedFrom != DerivedFrom {
				t.Errorf("meta.derived_from = %q, want %q", result.Definition.Meta.DerivedFrom, DerivedFrom)
			}
			if !strings.Contains(string(result.Body), "derived_from: cardigann") {
				t.Errorf("converted body does not contain derived_from: cardigann:\n%s", result.Body)
			}
			parsed, err := definition.Parse(result.Body)
			if err != nil {
				t.Fatalf("converted body does not parse: %v", err)
			}
			if !parsed.IsDerived() {
				t.Error("parsed converted definition is not marked as derived")
			}
		})
	}
}

func TestDerivedFromSurvivesAuthorOverride(t *testing.T) {
	results, _ := ConvertDirectory(context.Background(), "testdata", Options{Author: "synthetic-author"})
	if len(results) == 0 {
		t.Fatal("ConvertDirectory returned no successful conversions")
	}
	for _, converted := range results {
		if converted.Definition.Meta.Author != "synthetic-author" {
			t.Errorf("%s: meta.author = %q, want the override",
				converted.SourceID, converted.Definition.Meta.Author)
		}
		if converted.Definition.Meta.DerivedFrom != DerivedFrom {
			t.Errorf("%s: meta.derived_from = %q, want %q",
				converted.SourceID, converted.Definition.Meta.DerivedFrom, DerivedFrom)
		}
	}
}
