package smartfeed

import (
	"net/url"
	"strings"
	"testing"
)

const token = "SYNTHETICTOKEN0000000000000000000000"

func TestRecognizeAndSuggestT2(t *testing.T) {
	raw := "https://mircrew-releases.org/smartfeed/feed?u=1&e=" + token + "&f=51&f=26&t=1&d=3&x=200"
	info, ok := Recognize(raw)
	if !ok || info.User != "1" || info.Token != token || info.T != 1 {
		t.Fatalf("Recognize = %+v, %t", info, ok)
	}
	if !SuggestsAnyPost(raw) {
		t.Fatal("t=1 was not suggested for t=2")
	}
	normalized, changed := WithAnyPost(raw)
	if !changed {
		t.Fatal("WithAnyPost reported no change")
	}
	parsed, err := url.Parse(normalized)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	if query.Get("t") != "2" {
		t.Errorf("t = %q, want 2", query.Get("t"))
	}
	if query.Get("d") != "3" || query.Get("x") != "200" || query.Get("u") != "1" || query.Get("e") != token {
		t.Errorf("other parameters were not preserved: %v", query)
	}
	if forums := query["f"]; len(forums) != 2 || forums[0] != "51" || forums[1] != "26" {
		t.Errorf("f values = %v, want [51 26]", forums)
	}
}

func TestNonSmartfeedIsNotRecognized(t *testing.T) {
	for _, raw := range []string{
		"https://mircrew-releases.org/smartfeed/ui?sid=1",
		"https://mircrew-releases.org/smartfeed/feed?u=1",
		"https://example.org/rss?u=1&e=" + token,
	} {
		if IsSmartfeedURL(raw) {
			t.Errorf("%q was recognized as a Smartfeed", raw)
		}
	}
}

func TestCleanTitleHandlesCDATAAndEscapedHTML(t *testing.T) {
	cases := map[string]string{
		"Serie TV in corso :: Reacher - Stagione 3 (2026) 1080p :: Risposta di user":          "Reacher - Stagione 3 (2026) 1080p",
		"Film :: The Report (2019) 1080p :: Autore user":                                      "The Report (2019) 1080p",
		"<![CDATA[Stagioni complete :: Silo - Stagioni 1-3 [COMPLETA] :: Risposta di user]]>": "Silo - Stagioni 1-3 [COMPLETA]",
		"&lt;![CDATA[Film :: Dune (2024) 2160p]]&gt;":                                         "Dune (2024) 2160p",
		"<b>Serie TV</b> :: <i>VisionQuest</i> - Stagione 1 :: Reply by user":                 "VisionQuest - Stagione 1",
	}
	for raw, want := range cases {
		if got := CleanTitle(raw); got != want {
			t.Errorf("CleanTitle(%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestTopicURLAndForum(t *testing.T) {
	raw := "https://mircrew-releases.org/viewtopic.php?f=52&t=226850&p=999#p999"
	if got := TopicURL(raw); got != "https://mircrew-releases.org/viewtopic.php?f=52&t=226850" {
		t.Errorf("TopicURL = %q", got)
	}
	if got := ForumID(raw); got != 52 {
		t.Errorf("ForumID = %d, want 52", got)
	}
	if got := CategoryForForum(26); got != 2040 {
		t.Errorf("CategoryForForum(26) = %d", got)
	}
}

func TestExpiredErrorAndUILinkAreTokenFree(t *testing.T) {
	raw := "https://mircrew-releases.org/smartfeed/feed?u=1&e=" + token + "&t=2"
	message := ExpiredError(raw).Error()
	if !strings.Contains(message, "https://mircrew-releases.org/smartfeed/ui") {
		t.Errorf("message = %q", message)
	}
	if strings.Contains(message, token) || strings.Contains(message, "u=1") {
		t.Errorf("message leaked the token: %q", message)
	}
}
