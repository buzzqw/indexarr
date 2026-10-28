package definition

import (
	"path/filepath"
	"strings"
	"testing"
)

func minimalDefinitionYAML(extra string) string {
	return `schema: 1
meta:
  id: test-definition
  name: Test Definition
  type: public
  protocol: torrent
  urls: [https://test.example/]
` + extra
}

func TestInvalidFixtures(t *testing.T) {
	cases := []struct {
		file     string
		fragment string
	}{
		{"bad-schema.yaml", "schema: must be 1"},
		{"bad-slug.yaml", "meta.id"},
		{"missing-urls.yaml", "meta.urls"},
		{"bad-protocol.yaml", "meta.protocol"},
		{"unknown-capability.yaml", "capabilities.tv-search"},
		{"unknown-field-type.yaml", "search.response.fields.size.type"},
		{"required-undefined.yaml", "search.response.required"},
		{"fixture-dotdot.yaml", "tests[0].fixture"},
	}

	for _, tc := range cases {
		t.Run(tc.file, func(t *testing.T) {
			_, err := Load(filepath.Join("testdata", "invalid", tc.file))
			if err == nil {
				t.Fatalf("expected %s to fail validation", tc.file)
			}
			if !strings.Contains(err.Error(), tc.fragment) {
				t.Errorf("error %q does not contain %q", err.Error(), tc.fragment)
			}
		})
	}
}

func TestValidationCollectsEveryProblem(t *testing.T) {
	body := `schema: 0
meta:
  id: BAD_ID
  name: ""
  type: nope
  protocol: nope
  urls: ["not-a-url", "/relative"]
settings:
  - { name: Dup }
  - { name: Dup }
categories:
  - { site: "1", standard: 999 }
capabilities:
  search: [q, weird]
search:
  request:
    get: /x
    post: /x
  pagination: { type: weird }
  response:
    type: yaml
    fields:
      a: { type: blob }
    required: [b]
tests:
  - { fixture: "/absolute/path", expect: { min_rows: -1 } }
  - { fixture: "a/../b" }
`
	err := (*Definition)(nil).Validate()
	if err == nil {
		t.Fatal("nil definition should not validate")
	}

	_, err = Parse([]byte(body))
	if err == nil {
		t.Fatal("expected validation to fail")
	}

	for _, fragment := range []string{
		"schema: must be 1",
		"meta.id:",
		"meta.name:",
		"meta.type:",
		"meta.protocol:",
		"meta.urls[0]:",
		"meta.urls[1]:",
		"settings[0].name:",
		"settings[1].name:",
		"categories[0].standard:",
		"capabilities.search[1]:",
		"search.request:",
		"search.pagination.type:",
		"search.response.type:",
		"search.response.fields.a.type:",
		"search.response.required[0]:",
		"tests[0].fixture:",
		"tests[0].expect.min_rows:",
		"tests[1].fixture:",
	} {
		if !strings.Contains(err.Error(), fragment) {
			t.Errorf("error does not contain %q:\n%s", fragment, err)
		}
	}
}

func TestConnectorMayOmitSearch(t *testing.T) {
	body := `schema: 1
connector: torznab
meta:
  id: connector-only
  name: Connector Only
  type: public
  protocol: torrent
  urls: [https://connector.example/]
`
	if _, err := Parse([]byte(body)); err != nil {
		t.Fatalf("connector-based definition should validate without search: %v", err)
	}
}

func TestSearchRequiredWithoutConnector(t *testing.T) {
	body := `schema: 1
meta:
  id: no-search
  name: No Search
  type: public
  protocol: torrent
  urls: [https://no-search.example/]
`
	_, err := Parse([]byte(body))
	if err == nil {
		t.Fatal("expected validation to fail")
	}
	for _, fragment := range []string{"search.request:", "search.response.type:"} {
		if !strings.Contains(err.Error(), fragment) {
			t.Errorf("error does not contain %q: %v", fragment, err)
		}
	}
}

func TestAuthValidation(t *testing.T) {
	cases := []struct {
		name     string
		extra    string
		fragment string
	}{
		{
			name: "both get and post",
			extra: `auth:
  strategy: form
  steps:
    - { get: /login, post: /login }
`,
			fragment: "auth.steps[0]: get and post are mutually exclusive",
		},
		{
			name: "neither get nor post",
			extra: `auth:
  strategy: form
  steps:
    - { form: { user: x } }
`,
			fragment: "auth.steps[0]: must have get or post",
		},
		{
			name:     "unknown strategy",
			extra:    "auth: { strategy: magic }\n",
			fragment: "auth.strategy:",
		},
		{
			name: "empty failure condition",
			extra: `auth:
  strategy: form
  failure: [{}]
`,
			fragment: "auth.failure[0]:",
		},
		{
			name: "logged_in without selector",
			extra: `auth:
  strategy: form
  logged_in: { status: 200 }
`,
			fragment: "auth.logged_in: needs a selector",
		},
		{
			name: "search.blocked without selector",
			extra: `search:
  request: { get: /search }
  response:
    rows: { selector: tr }
    fields: { title: { selector: a } }
  blocked: { status: 200 }
`,
			fragment: "search.blocked: needs a selector",
		},
		{
			name: "search.interval without cache",
			extra: `search:
  request: { get: /search }
  response:
    rows: { selector: tr }
    fields: { title: { selector: a } }
  interval: 60s
`,
			fragment: "search.interval: needs search.cache",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse([]byte(minimalDefinitionYAML(tc.extra)))
			if err == nil {
				t.Fatal("expected validation to fail")
			}
			if !strings.Contains(err.Error(), tc.fragment) {
				t.Errorf("error %q does not contain %q", err.Error(), tc.fragment)
			}
		})
	}
}

func TestSearchValidation(t *testing.T) {
	cases := []struct {
		name     string
		extra    string
		fragment string
	}{
		{
			name: "both get and post",
			extra: `search:
  request: { get: /search, post: /search }
  response: { type: html }
`,
			fragment: "search.request: get and post are mutually exclusive",
		},
		{
			name: "bad pagination type",
			extra: `search:
  request: { get: /search }
  pagination: { type: randoms }
  response: { type: html }
`,
			fragment: "search.pagination.type:",
		},
		{
			name: "bad response type",
			extra: `search:
  request: { get: /search }
  response: { type: yaml }
`,
			fragment: "search.response.type:",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse([]byte(minimalDefinitionYAML(tc.extra)))
			if err == nil {
				t.Fatal("expected validation to fail")
			}
			if !strings.Contains(err.Error(), tc.fragment) {
				t.Errorf("error %q does not contain %q", err.Error(), tc.fragment)
			}
		})
	}
}
