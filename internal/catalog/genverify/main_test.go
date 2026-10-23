package main

import (
	"strings"
	"testing"
)

func TestHTMLTitleHandlesUnicodeAndAttributes(t *testing.T) {
	body := []byte(`<html><head><TITLE lang="it">  Città  di Roma </TITLE></head></html>`)
	if got, want := htmlTitle(body), "Città di Roma"; got != want {
		t.Errorf("htmlTitle = %q, want %q", got, want)
	}
	// A lowercase mapping can change byte length; the title slicer must not
	// use the lowercased string's indices.
	body = []byte(`<title>İstanbul tracker</title>`)
	if got, want := htmlTitle(body), "İstanbul tracker"; got != want {
		t.Errorf("htmlTitle = %q, want %q", got, want)
	}
	if got := htmlTitle([]byte(`no title`)); got != "" {
		t.Errorf("htmlTitle without a title = %q", got)
	}
}

func TestFamilyMarkers(t *testing.T) {
	tests := map[string]string{
		`<script src="/jscript/xbtit.js"></script>`: "xbtit",
		`Powered by NexusPHP`:                       "nexusphp",
		`<title>Login :: GazelleGames.net</title>`:  "gazelle",
		`<form action="takelogin.php">`:             "nexusphp",
		`<h1>Torrent Trader</h1>`:                   "torrenttrader",
		`<div class="Jackett">UNIT3D</div>`:         "unit3d",
	}
	for body, want := range tests {
		found := familyMarkers([]byte(body))
		ok := false
		for _, family := range found {
			if family == want {
				ok = true
			}
		}
		if !ok {
			t.Errorf("familyMarkers(%q) = %v, want %q", body, found, want)
		}
	}
}

func TestNormalizeURL(t *testing.T) {
	if got, want := normalizeURL("https://www.Example.test/path/"), "https://example.test/path"; got != want {
		t.Errorf("normalizeURL = %q, want %q", got, want)
	}
	if got := normalizeURL("not a url"); got != "" {
		t.Errorf("normalizeURL(invalid) = %q", got)
	}
	if !strings.Contains(normalizeURL("https://example.test/"), "example.test") {
		t.Error("normalizeURL dropped the host")
	}
}
