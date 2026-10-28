package engine

import "testing"

func TestDropTopicsOfAnotherSeason(t *testing.T) {
	items := []Item{
		{Title: "New Tricks - Stagione 7 [COMPLETA]"},
		{Title: "New Tricks - Stagione 1 [COMPLETA]"},
		{Title: "New Tricks - Miniserie"},
		{Title: "New Tricks S02 720p"},
	}
	kept := dropTopicsOfAnotherSeason(append([]Item(nil), items...), SearchRequest{Mode: ModeTV, Query: "New Tricks", Season: 1})
	if len(kept) != 2 || kept[0].Title != items[1].Title || kept[1].Title != items[2].Title {
		t.Fatalf("season 1 kept %+v", kept)
	}
	// A free-text query names the season itself ("New Tricks S02E03").
	kept = dropTopicsOfAnotherSeason(append([]Item(nil), items...), SearchRequest{Mode: ModeSearch, Query: "New Tricks S02E03"})
	if len(kept) != 2 || kept[1].Title != items[3].Title {
		t.Fatalf("free-text S02E03 kept %+v", kept)
	}
	// Without a season every topic stays.
	if kept = dropTopicsOfAnotherSeason(append([]Item(nil), items...), SearchRequest{Mode: ModeTV, Query: "New Tricks"}); len(kept) != 4 {
		t.Fatalf("no season kept %d topics, want 4", len(kept))
	}
}
