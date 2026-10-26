package engine

import (
	"testing"

	"github.com/buzzqw/indexarr/internal/definition"
)

func TestRequestedSeasonEpisodeFallsBackToTitle(t *testing.T) {
	c := &DefinitionConnector{}
	season, episode := c.requestedSeasonEpisode(Item{Title: "Silo S03E01 1080p HEVC x265"})
	if season != 3 || episode != 1 {
		t.Fatalf("season/episode = %d/%d, want 3/1", season, episode)
	}
	season, episode = c.requestedSeasonEpisode(Item{Title: "Silo - Stagione 3 (2026) [COMPLETA]"})
	if season != 3 || episode != 0 {
		t.Fatalf("season pack = %d/%d, want 3/0", season, episode)
	}
}

func TestChooseCandidateUsesTheEpisodeFromTheTitle(t *testing.T) {
	c := &DefinitionConnector{def: &definition.Definition{
		Download: &definition.Download{Select: "season_episode"},
	}}
	candidates := []resolvedDownload{
		{Magnet: "magnet:?xt=urn:btih:aaa&dn=Silo.S03.1080p.ITA.ENG"},
		{Magnet: "magnet:?xt=urn:btih:bbb&dn=Silo.S03E01.1080p.ITA.ENG"},
		{Magnet: "magnet:?xt=urn:btih:ccc&dn=Silo.S03E02.1080p.ITA.ENG"},
	}
	chosen, ok := c.chooseCandidate("Silo S03E01 1080p", candidates, 3, 1)
	if !ok || chosen.Magnet != candidates[1].Magnet {
		t.Fatalf("chosen = %q, want the S03E01 magnet", chosen.Magnet)
	}

	// A season-pack title has episode 0 and must pick the season magnet.
	chosen, ok = c.chooseCandidate("Silo - Stagione 3 [COMPLETA]", candidates, 3, 0)
	if !ok || chosen.Magnet != candidates[0].Magnet {
		t.Fatalf("season pack chosen = %q, want the season magnet", chosen.Magnet)
	}
}
