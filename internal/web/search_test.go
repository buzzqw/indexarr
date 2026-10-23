package web

import (
	"strings"
	"testing"
)

func TestParseSearchTermsNormal(t *testing.T) {
	include, exclude, err := parseSearchTerms("The Show -1080p -CAM", false)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(include, ",") != "the,show" {
		t.Errorf("include = %#v", include)
	}
	if strings.Join(exclude, ",") != "1080p,cam" {
		t.Errorf("exclude = %#v", exclude)
	}

	include, exclude, err = parseSearchTerms("-1080p", false)
	if err != nil || len(include) != 0 || len(exclude) != 1 || exclude[0] != "1080p" {
		t.Errorf("only exclusion = %#v / %#v / %v", include, exclude, err)
	}

	include, exclude, err = parseSearchTerms("-", false)
	if err != nil || len(include) != 0 || len(exclude) != 0 {
		t.Errorf("lone hyphen = %#v / %#v / %v", include, exclude, err)
	}
}

func TestParseSearchTermsRegex(t *testing.T) {
	include, exclude, err := parseSearchTerms(`S0[12]E0[12] -720p`, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(include) != 1 || include[0] != "(?i)S0[12]E0[12]" {
		t.Errorf("include = %#v", include)
	}
	if len(exclude) != 1 || exclude[0] != "(?i)720p" {
		t.Errorf("exclude = %#v", exclude)
	}

	include, exclude, err = parseSearchTerms(`"The Show" -cam`, true)
	if err != nil || len(include) != 1 || include[0] != "(?i)The Show" || len(exclude) != 1 {
		t.Errorf("quoted = %#v / %#v / %v", include, exclude, err)
	}

	include, _, err = parseSearchTerms(`/foo|bar/`, true)
	if err != nil || len(include) != 1 || include[0] != "(?i)foo|bar" {
		t.Errorf("slash pattern = %#v / %v", include, err)
	}

	if _, _, err := parseSearchTerms(`[`, true); err == nil {
		t.Error("invalid regex accepted")
	}
	if _, _, err := parseSearchTerms(`foo -[`, true); err == nil {
		t.Error("invalid excluded regex accepted")
	}
}

func TestSplitRegexTerms(t *testing.T) {
	got := splitRegexTerms(`one "two words" three\ four`)
	want := []string{"one", "two words", `three\ four`}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("terms = %#v, want %#v", got, want)
	}
}
