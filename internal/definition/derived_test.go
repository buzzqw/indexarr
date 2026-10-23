package definition

import (
	"encoding/json"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// derivedDefinitionYAML is a minimal valid definition (connector-based, so it
// needs no search block) with room for extra meta fields.
func derivedDefinitionYAML(extraMeta string) string {
	return `schema: 1
connector: torznab
meta:
  id: derived-test
  name: Derived Test
  type: public
  protocol: torrent
  urls: [https://derived.example/]
` + extraMeta
}

func TestDerivedFromParsingAndMarshal(t *testing.T) {
	body := derivedDefinitionYAML("  derived_from: cardigann\n")
	def, err := Parse([]byte(body))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if def.Meta.DerivedFrom != "cardigann" {
		t.Fatalf("meta.derived_from = %q, want cardigann", def.Meta.DerivedFrom)
	}
	if !def.IsDerived() {
		t.Error("IsDerived() = false for a definition with derived_from")
	}

	encoded, err := yaml.Marshal(def)
	if err != nil {
		t.Fatalf("yaml.Marshal: %v", err)
	}
	if !strings.Contains(string(encoded), "derived_from: cardigann") {
		t.Errorf("YAML output lost derived_from:\n%s", encoded)
	}

	jsonBody, err := json.Marshal(def)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	if !strings.Contains(string(jsonBody), `"derived_from":"cardigann"`) {
		t.Errorf("JSON output lost derived_from: %s", jsonBody)
	}
}

func TestDerivedFromJSONRoundTrip(t *testing.T) {
	body := derivedDefinitionYAML("  derived_from: cardigann\n")
	def, err := Parse([]byte(body))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	jsonBody, err := json.Marshal(def)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	var decoded Definition
	if err := json.Unmarshal(jsonBody, &decoded); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	if decoded.Meta.DerivedFrom != "cardigann" {
		t.Errorf("decoded meta.derived_from = %q, want cardigann", decoded.Meta.DerivedFrom)
	}
}

func TestMirrorParsingAndValidation(t *testing.T) {
	def, err := Parse([]byte(derivedDefinitionYAML("  mirror: unofficial\n")))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if def.Meta.Mirror != "unofficial" {
		t.Fatalf("meta.mirror = %q", def.Meta.Mirror)
	}
	if _, err := Parse([]byte(derivedDefinitionYAML("  mirror: maybe\n"))); err == nil {
		t.Fatal("invalid mirror value was accepted")
	}
}

func TestDerivedFromValidation(t *testing.T) {
	if _, err := Parse([]byte(derivedDefinitionYAML("  derived_from: cardigann\n"))); err != nil {
		t.Fatalf("valid derived_from was rejected: %v", err)
	}

	_, err := Parse([]byte(derivedDefinitionYAML("  derived_from: Cardigann Import\n")))
	if err == nil {
		t.Fatal("Parse accepted a derived_from that is not a slug")
	}
	if !strings.Contains(err.Error(), "meta.derived_from") {
		t.Errorf("error does not mention meta.derived_from: %v", err)
	}
}

func TestIsDerived(t *testing.T) {
	cases := []struct {
		name string
		def  *Definition
		want bool
	}{
		{"nil", nil, false},
		{"plain", &Definition{Meta: Meta{Author: "indexarr"}}, false},
		{"derived_from", &Definition{Meta: Meta{DerivedFrom: "cardigann"}}, true},
		{"author exact", &Definition{Meta: Meta{Author: "cardigann-import"}}, true},
		{"author mixed case", &Definition{Meta: Meta{Author: "Cardigann-Import"}}, true},
		{"author uppercase", &Definition{Meta: Meta{Author: "CARDIGANN-IMPORT"}}, true},
		{"author padded", &Definition{Meta: Meta{Author: "  cardigann-import "}}, true},
		{"other author", &Definition{Meta: Meta{Author: "cardigann-importer"}}, false},
		{"blank derived_from", &Definition{Meta: Meta{DerivedFrom: "   "}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.def.IsDerived(); got != tc.want {
				t.Errorf("IsDerived() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestJSONSchemaHasDerivedFrom(t *testing.T) {
	var doc struct {
		Defs map[string]struct {
			Properties map[string]json.RawMessage `json:"properties"`
		} `json:"$defs"`
	}
	if err := json.Unmarshal(JSONSchema(), &doc); err != nil {
		t.Fatalf("Unmarshal schema: %v", err)
	}
	meta, ok := doc.Defs["meta"]
	if !ok {
		t.Fatal("schema has no meta definition")
	}
	if _, ok := meta.Properties["derived_from"]; !ok {
		t.Error("schema meta.properties is missing derived_from")
	}
}
