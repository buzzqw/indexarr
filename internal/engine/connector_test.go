package engine

import "testing"

func TestCapabilitiesSupports(t *testing.T) {
	caps := Capabilities{
		Protocol: ProtocolTorrent,
		Modes:    []Mode{ModeSearch, ModeTV},
		Params: map[Mode][]string{
			ModeTV: {ParamQuery, ParamSeason, ParamEpisode},
		},
	}

	if !caps.Supports(ModeSearch) || !caps.Supports(ModeTV) {
		t.Error("declared modes are not supported")
	}
	if caps.Supports(ModeMovie) {
		t.Error("undeclared mode is supported")
	}
	if !caps.SupportsParam(ModeTV, ParamSeason) {
		t.Error("declared parameter is not supported")
	}
	if caps.SupportsParam(ModeTV, ParamIMDbID) {
		t.Error("undeclared parameter is supported")
	}
	if caps.SupportsParam(ModeMovie, ParamQuery) {
		t.Error("parameter without a mode entry is supported")
	}
}

func TestProtocolValid(t *testing.T) {
	for _, protocol := range []Protocol{ProtocolTorrent, ProtocolUsenet} {
		if !protocol.Valid() {
			t.Errorf("%q is not valid", protocol)
		}
	}
	for _, protocol := range []Protocol{"", "http", "TORRENT"} {
		if protocol.Valid() {
			t.Errorf("%q is valid", protocol)
		}
	}
}

func TestModeValid(t *testing.T) {
	for _, mode := range []Mode{ModeSearch, ModeTV, ModeMovie, ModeMusic, ModeBook} {
		if !mode.Valid() {
			t.Errorf("%q is not valid", mode)
		}
	}
	for _, mode := range []Mode{"", "tv-search", "SEARCH"} {
		if mode.Valid() {
			t.Errorf("%q is valid", mode)
		}
	}
}

func TestValidParam(t *testing.T) {
	for _, param := range []string{
		ParamQuery, ParamSeason, ParamEpisode, ParamIMDbID, ParamTMDbID,
		ParamTVDbID, ParamTVMazeID, ParamRID, ParamArtist, ParamAlbum,
		ParamAuthor, ParamTitle, ParamYear, ParamGenre,
	} {
		if !ValidParam(param) {
			t.Errorf("known parameter %q rejected", param)
		}
	}
	for _, param := range []string{"", "page", "Q", "categories"} {
		if ValidParam(param) {
			t.Errorf("unknown parameter %q accepted", param)
		}
	}
}
