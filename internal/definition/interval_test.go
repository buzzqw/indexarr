package definition

import (
	"strings"
	"testing"
	"time"
)

func intervalDefinition(interval string) []byte {
	extra := ""
	if interval != "" {
		extra = "  interval: " + interval + "\n"
	}
	return []byte(`schema: 1
connector: rss
meta:
  id: interval-test
  name: Interval Test
  type: public
  protocol: torrent
  urls: ["https://feed.example/"]
` + extra)
}

func TestParseMetaInterval(t *testing.T) {
	def, err := Parse(intervalDefinition("15m"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got := def.Meta.Interval.Duration(); got != 15*time.Minute {
		t.Fatalf("meta.interval = %v, want 15m", got)
	}

	empty, err := Parse(intervalDefinition(""))
	if err != nil {
		t.Fatalf("Parse without interval: %v", err)
	}
	if empty.Meta.Interval.Duration() != 0 {
		t.Fatalf("missing meta.interval = %v, want 0", empty.Meta.Interval.Duration())
	}
}

func TestParseMetaIntervalRejectsShortAndInvalid(t *testing.T) {
	for _, raw := range []string{"30s", "59s", "-15m", "soon"} {
		_, err := Parse(intervalDefinition(raw))
		if err == nil {
			t.Errorf("meta.interval %q was accepted, want an error", raw)
			continue
		}
		if !strings.Contains(err.Error(), "meta.interval") && raw != "soon" {
			t.Errorf("meta.interval %q error does not mention the field: %v", raw, err)
		}
	}
}
