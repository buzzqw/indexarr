package i18n

// defaultEnglish holds the keys added by the manual live search before the
// packaged locale files were updated. T falls back here after English, and
// Keys reports them, so templates keep their catalogue contract while the
// translations can follow. The locale JSON files are intentionally untouched.
var defaultEnglish = map[string]string{
	"releases.liveTitle":          "Live search on indexers",
	"releases.liveIntro":          "Ask the selected sites now, store what they return and show it below. This is the same fan-out an app triggers, run on demand.",
	"releases.liveModeHelp":       "Free search finds any title. TV, movie, music and book use the fields each site declares.",
	"releases.liveQueryHelp":      "Words to look for. A season or episode can be typed here or entered in the fields below.",
	"releases.liveYear":           "Year",
	"releases.liveYearHelp":       "Release year. A movie search uses it directly; a site without movie search receives it in the text.",
	"releases.liveSeasonHelp":     "Season number for a TV search. Leave it empty when the text already carries SxxEyy.",
	"releases.liveEpisodeHelp":    "Episode number for a TV search. A season pack also matches an episode request.",
	"releases.liveIdsHelp":        "Use one identifier when you already know it. It replaces the text on the sites that support it.",
	"releases.liveArtist":         "Artist",
	"releases.liveArtistHelp":     "Music artist or band. Use it alone or together with the album.",
	"releases.liveAlbum":          "Album",
	"releases.liveAlbumHelp":      "Album title for a music search.",
	"releases.liveAuthor":         "Author",
	"releases.liveAuthorHelp":     "Book or audiobook author for a book search.",
	"releases.liveTitleField":     "Title",
	"releases.liveTitleFieldHelp": "Exact title parameter, used by sites that search a title field instead of free text.",
	"releases.liveIndexersHelp":   "One or more sites to ask. Only enabled sites that can search live appear here.",
	"releases.liveCategoryHelp":   "Restrict the request to one standard category. Leave it empty for every category.",
	"releases.liveSubmit":         "Search live",
	"releases.liveFooter":         "Live search asks the sites now instead of reading only the catalogue. Incoming releases are stored, so the app searches and the next page load can reuse them. Timed-out sites keep working in the background.",
	"releases.liveValidation":     "Type a query or an IMDb, TMDb, TVDb ID, artist, album, author or title.",
	"releases.liveNoIndexers":     "Select at least one indexer to search.",
	"releases.liveUnavailable":    "Live search is not available: the native indexer service is not running.",
	"releases.liveFailed":         "Live search failed: %s",
	"releases.liveOutcomes":       "What each indexer did",
	"releases.liveStored":         "%d release(s) stored by this search.",
	"releases.liveAnsweredHelp":   "Answered before the timeout. Its results appear below.",
	"releases.liveTimeoutHelp":    "Still looking in the background. Its results may appear after a reload.",
	"releases.liveSkippedHelp":    "Skipped: this site cannot answer the selected search type.",
	"releases.bulkGrab":           "Send selected",
	"releases.bulkGrabConfirm":    "Send the selected releases to the download client?",
	"releases.bulkGrabHint":       "Select the releases to send to the download client.",
	"releases.bulkGrabResult":     "%d sent, %d failed",
	"releases.bulkInvalidOp":      "Unknown bulk action.",
}
