package definition

import (
	"encoding/json"
	"strings"
	"testing"
)

func downloadDefinitionYAML(download string) string {
	return minimalDefinitionYAML(`search:
  request: { get: /search }
  response:
    type: html
    fields:
      title: { selector: "a.title" }
      details: { selector: "a.title", attr: href, type: url }
`) + download
}

func TestDownloadSectionParse(t *testing.T) {
	def, err := Load("testdata/download.yaml")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if def.Download == nil {
		t.Fatal("download section is nil")
	}
	download := def.Download
	if download.ResolveOn != "search" {
		t.Errorf("resolve_on = %q", download.ResolveOn)
	}
	if download.From != "details" {
		t.Errorf("from = %q", download.From)
	}
	if download.MaxConcurrent != 2 {
		t.Errorf("max_concurrent = %d", download.MaxConcurrent)
	}
	if len(download.Extract) != 2 {
		t.Fatalf("extract = %d, want 2", len(download.Extract))
	}
	magnet := download.Extract[0].Magnet
	if magnet == nil || len(magnet.Selector) != 1 || magnet.Selector[0] != `a[href^="magnet:"]` {
		t.Errorf("magnet target = %+v", download.Extract[0])
	}
	torrent := download.Extract[1].Torrent
	if torrent == nil || torrent.Type != "url" {
		t.Errorf("torrent target = %+v", download.Extract[1])
	}
}

func TestDownloadSectionDefaults(t *testing.T) {
	body := downloadDefinitionYAML(`download:
  extract:
    - torrent: { selector: 'a[href$=".torrent"]', attr: href, type: url }
`)
	def, err := Parse([]byte(body))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if def.Download == nil {
		t.Fatal("download section is nil")
	}
	if def.Download.ResolveOn != "" || def.Download.From != "" || def.Download.MaxConcurrent != 0 {
		t.Errorf("defaults should stay unset in the struct: %+v", def.Download)
	}
}

func TestDownloadSectionValidation(t *testing.T) {
	cases := []struct {
		name     string
		download string
		fragment string
	}{
		{
			name: "unknown from",
			download: `download:
  from: nope
  extract:
    - magnet: { selector: 'a[href^="magnet:"]', attr: href }
`,
			fragment: `download.from: field "nope"`,
		},
		{
			name: "empty extract",
			download: `download:
  extract: []
`,
			fragment: "download.extract: at least one target",
		},
		{
			name: "both magnet and torrent",
			download: `download:
  extract:
    - magnet: { selector: "a.magnet", attr: href }
      torrent: { selector: "a.torrent", attr: href }
`,
			fragment: "download.extract[0]: magnet and torrent are mutually exclusive",
		},
		{
			name: "neither magnet nor torrent",
			download: `download:
  extract:
    - {}
`,
			fragment: "download.extract[0]: must set magnet or torrent",
		},
		{
			name: "negative max_concurrent",
			download: `download:
  max_concurrent: -1
  extract:
    - magnet: { selector: "a.magnet", attr: href }
`,
			fragment: "download.max_concurrent:",
		},
		{
			name: "max_concurrent above the limit",
			download: `download:
  max_concurrent: 9
  extract:
    - magnet: { selector: "a.magnet", attr: href }
`,
			fragment: "download.max_concurrent:",
		},
		{
			name: "unknown resolve_on",
			download: `download:
  resolve_on: sometimes
  extract:
    - magnet: { selector: "a.magnet", attr: href }
`,
			fragment: "download.resolve_on:",
		},
		{
			name: "target without selector or path",
			download: `download:
  extract:
    - magnet: { attr: href }
`,
			fragment: "download.extract[0].magnet: needs selector or path",
		},
		{
			name: "unknown target type",
			download: `download:
  extract:
    - torrent: { selector: "a.torrent", attr: href, type: blob }
`,
			fragment: "download.extract[0].torrent.type:",
		},
		{
			name: "bad replace filter",
			download: `download:
  extract:
    - magnet: { selector: "a.magnet", attr: href, filters: [{ replace: nope }] }
`,
			fragment: "download.extract[0].magnet.filters[0]: replace argument",
		},
		{
			name: "unknown filter",
			download: `download:
  extract:
    - magnet: { selector: "a.magnet", attr: href, filters: [explode] }
`,
			fragment: "unknown filter",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse([]byte(downloadDefinitionYAML(tc.download)))
			if err == nil {
				t.Fatal("expected validation to fail")
			}
			if !strings.Contains(err.Error(), tc.fragment) {
				t.Errorf("error %q does not contain %q", err.Error(), tc.fragment)
			}
		})
	}
}

func TestDownloadDefaultFromMustBeDeclared(t *testing.T) {
	body := minimalDefinitionYAML(`search:
  request: { get: /search }
  response:
    type: html
    fields:
      title: { selector: "a.title" }
download:
  extract:
    - magnet: { selector: "a.magnet", attr: href }
`)
	_, err := Parse([]byte(body))
	if err == nil {
		t.Fatal("expected validation to fail when the default details field is missing")
	}
	if !strings.Contains(err.Error(), `download.from: field "details"`) {
		t.Errorf("error %q does not mention download.from", err.Error())
	}
}

func TestDownloadJSONRoundTrip(t *testing.T) {
	body := []byte(`{"resolve_on":"grab","from":"page","max_concurrent":4,"extract":[{"magnet":{"selector":"a.magnet","attr":"href"}},{"torrent":{"path":"file.url","type":"url"}}]}`)
	var download Download
	if err := json.Unmarshal(body, &download); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if download.ResolveOn != "grab" || download.From != "page" || download.MaxConcurrent != 4 {
		t.Errorf("download = %+v", download)
	}
	if len(download.Extract) != 2 || download.Extract[0].Magnet == nil || download.Extract[1].Torrent == nil {
		t.Fatalf("extract = %+v", download.Extract)
	}
	if download.Extract[1].Torrent.Path != "file.url" || download.Extract[1].Torrent.Type != "url" {
		t.Errorf("torrent target = %+v", download.Extract[1].Torrent)
	}

	encoded, err := json.Marshal(download)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var roundTripped Download
	if err := json.Unmarshal(encoded, &roundTripped); err != nil {
		t.Fatalf("round-trip Unmarshal: %v", err)
	}
	if roundTripped.ResolveOn != download.ResolveOn || len(roundTripped.Extract) != 2 {
		t.Errorf("round-trip = %+v", roundTripped)
	}
}

func TestDownloadStrictUnknownField(t *testing.T) {
	body := downloadDefinitionYAML(`download:
  resolve_on: search
  bogus: true
  extract:
    - magnet: { selector: "a.magnet", attr: href }
`)
	_, err := Parse([]byte(body))
	if err == nil {
		t.Fatal("expected strict decoding to reject the unknown download key")
	}
	if !strings.Contains(err.Error(), "bogus") {
		t.Errorf("error does not mention the unknown key: %v", err)
	}
}
