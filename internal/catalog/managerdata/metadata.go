// Package managerdata holds the public metadata snapshot of a Jackett or
// Prowlarr indexer catalog.
//
// It is used only by the build-time catalog generators (gensites, genverify)
// to read the committed JSON snapshots. The running indexarr binary never
// imports this package and never contacts a manager: Jackett and Prowlarr are
// replaced by indexarr, not used as runtime sources.
package managerdata

// CatalogIndexer is the small slice of a manager's indexer metadata that the
// site catalog needs: display fields, site address, privacy type, language and
// Torznab categories. It carries no credentials and no Cardigann definition.
type CatalogIndexer struct {
	SourceID    string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	URL         string   `json:"url"`
	URLs        []string `json:"urls,omitempty"`
	Type        string   `json:"type"`
	Language    string   `json:"language,omitempty"`
	Categories  []int    `json:"categories,omitempty"`
	Configured  bool     `json:"configured"`
	Priority    int      `json:"priority,omitempty"`
}
