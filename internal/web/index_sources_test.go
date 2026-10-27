package web

import (
	"testing"

	"github.com/buzzqw/indexarr/internal/definition"
)

func TestIndexSourceViewsListsTVAndMovieURLs(t *testing.T) {
	def, err := definition.Parse([]byte(`schema: 1
meta:
  id: index-view
  name: Index view
  type: private
  protocol: ed2k
  urls:
    - https://ddunlimited.net/
capabilities:
  search: [q]
search:
  index:
    forum: /viewforum.php?f=1988
    response:
      type: html
      rows: { selector: "a.postlink-local" }
      fields:
        title: { self: true }
        details: { self: true, attr: href, type: url }
    pages:
      - { get: "/viewtopic.php?t=61463", standard: 5000 }
      - { get: "/viewtopic.php?t=3941486", letter: A, standard: 2000 }
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	views := indexSourceViews(def, "Serie TV", "Film")
	if len(views) != 2 {
		t.Fatalf("views = %+v", views)
	}
	if views[0].URL != "https://ddunlimited.net/viewtopic.php?t=61463" || views[1].URL != "https://ddunlimited.net/viewforum.php?f=1988" {
		t.Errorf("views = %+v", views)
	}
}
