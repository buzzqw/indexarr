package definition

import (
	"encoding/json"
	"strings"
	"testing"
)

func sessionDefinitionYAML(extra string) string {
	return minimalDefinitionYAML(`search:
  request: { get: /search }
  response:
    type: html
    fields:
      title: { selector: "a.title" }
      details: { selector: "a.title", attr: href, type: url }
`) + extra
}

func TestSessionSectionParse(t *testing.T) {
	body := sessionDefinitionYAML(`session:
  mode: flaresolverr
  endpoint: "{{ settings.flaresolverr_url }}"
  name_prefix: mircrew-idx
  max_age: 12h
  timeout: 60s
`)
	def, err := Parse([]byte(body))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if def.Session == nil {
		t.Fatal("session section is nil")
	}
	if def.Session.Mode != "flaresolverr" || def.Session.NamePrefix != "mircrew-idx" {
		t.Errorf("session = %+v", def.Session)
	}
	if def.Session.MaxAge.Duration().Hours() != 12 {
		t.Errorf("max_age = %v", def.Session.MaxAge.Duration())
	}
	if def.Session.Timeout.Duration().Seconds() != 60 {
		t.Errorf("timeout = %v", def.Session.Timeout.Duration())
	}
}

func TestSessionSectionValidation(t *testing.T) {
	cases := []struct {
		name   string
		extra  string
		expect string
	}{
		{
			name: "unknown mode",
			extra: `session:
  mode: browser
`,
			expect: "session.mode:",
		},
		{
			name: "bad endpoint",
			extra: `session:
  endpoint: "not a url"
`,
			expect: "session.endpoint:",
		},
		{
			name: "bad prefix",
			extra: `session:
  name_prefix: "MirCrew Idx"
`,
			expect: "session.name_prefix:",
		},
		{
			name: "negative max age",
			extra: `session:
  max_age: -1h
`,
			expect: "session.max_age:",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse([]byte(sessionDefinitionYAML(tc.extra)))
			if err == nil {
				t.Fatal("expected validation to fail")
			}
			if !strings.Contains(err.Error(), tc.expect) {
				t.Errorf("error %q does not contain %q", err.Error(), tc.expect)
			}
		})
	}
}

func TestDownloadBeforeAndSelectParse(t *testing.T) {
	body := sessionDefinitionYAML(`download:
  resolve_on: grab
  from: details
  select: season_episode
  before:
    wait: 2s
    extract:
      token: { selector: 'input[name="form_token"]', attr: value }
    action:
      post: /thanks.php
      form:
        thanks: "9"
        form_token: "{{ token }}"
  extract:
    - magnet: { selector: 'a[href^="magnet:"]', attr: href }
      all: true
`)
	def, err := Parse([]byte(body))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if def.Download.Select != "season_episode" {
		t.Errorf("select = %q", def.Download.Select)
	}
	if !def.Download.Extract[0].All {
		t.Error("all is not set")
	}
	before := def.Download.Before
	if before == nil || before.Action.Post != "/thanks.php" || before.Wait.Duration().Seconds() != 2 {
		t.Fatalf("before = %+v", before)
	}
	if before.Extract["token"].Selector == "" {
		t.Errorf("extract = %+v", before.Extract)
	}
}

func TestDownloadBeforeRejectsResolveOnSearch(t *testing.T) {
	body := sessionDefinitionYAML(`download:
  resolve_on: search
  from: details
  before:
    action: { get: /thanks.php }
  extract:
    - magnet: { selector: 'a[href^="magnet:"]', attr: href }
`)
	_, err := Parse([]byte(body))
	if err == nil {
		t.Fatal("expected validation to fail")
	}
	if !strings.Contains(err.Error(), "download.before: requires resolve_on grab") {
		t.Errorf("error = %v", err)
	}
}

func TestDownloadBeforeValidation(t *testing.T) {
	cases := []struct {
		name   string
		before string
		expect string
	}{
		{
			name:   "empty action",
			before: "    action: {}",
			expect: "download.before.action: must have get or post",
		},
		{
			name:   "get and post",
			before: "    action: { get: /a, post: /b }",
			expect: "download.before.action: get and post are mutually exclusive",
		},
		{
			name:   "empty extractor",
			before: "    extract:\n      token: {}\n    action: { get: /a }",
			expect: "download.before.extract.token: needs selector, json or regex",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := sessionDefinitionYAML("download:\n  resolve_on: grab\n  from: details\n  before:\n" + tc.before + "\n  extract:\n    - magnet: { selector: 'a[href^=\"magnet:\"]', attr: href }\n")
			_, err := Parse([]byte(body))
			if err == nil {
				t.Fatal("expected validation to fail")
			}
			if !strings.Contains(err.Error(), tc.expect) {
				t.Errorf("error %q does not contain %q", err.Error(), tc.expect)
			}
		})
	}
}

func TestDownloadSelectRoundTrip(t *testing.T) {
	body := []byte(`{"select":"season_episode","extract":[{"magnet":{"selector":"a.magnet","attr":"href"},"all":true}]}`)
	var download Download
	if err := json.Unmarshal(body, &download); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if download.Select != "season_episode" || !download.Extract[0].All {
		t.Errorf("download = %+v", download)
	}
	encoded, err := json.Marshal(download)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var round Download
	if err := json.Unmarshal(encoded, &round); err != nil {
		t.Fatalf("round-trip: %v", err)
	}
	if round.Select != "season_episode" || !round.Extract[0].All {
		t.Errorf("round-trip = %+v", round)
	}
}

func TestStrictUnknownSessionField(t *testing.T) {
	_, err := Parse([]byte(sessionDefinitionYAML(`session:
  mode: flaresolverr
  bogus: true
`)))
	if err == nil || !strings.Contains(err.Error(), "bogus") {
		t.Fatalf("expected strict decoding to reject bogus: %v", err)
	}
}
