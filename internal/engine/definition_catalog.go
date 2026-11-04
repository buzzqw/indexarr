package engine

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/buzzqw/indexarr/internal/definition"
)

// CatalogRequest asks for one page of a forum listing.
type CatalogRequest struct {
	// Forum is the site's forum id, as declared in catalog.forums.
	Forum string
	// Start is the first row of the page (the phpBB "start" parameter).
	Start int
	// PageSize is how many rows the page holds. Zero uses the definition's
	// catalog.page_size and then 50.
	PageSize int
}

// CatalogEntry is one topic of the local history: a thread or a series with
// its address. It has no magnet: the topic is opened only when a client grabs
// it, exactly as a search result of the same site.
type CatalogEntry struct {
	Title    string
	URL      string
	Forum    string
	Standard int
}

// CatalogResult is one listing page.
type CatalogResult struct {
	Entries []CatalogEntry
	// PageSize is the site's page size: the cursor advances by it, because
	// the page holds a few rows more than the topics (announcements and
	// stickies) that the definition skips.
	PageSize int
	// More is true while a page still lists topics. The walk asks one page
	// more than the last one: it comes back empty and closes the forum.
	More bool
}

// Catalog reads one page of the definition's topic history. The page is a
// plain listing: it is fetched like a latest page (login included) and parsed
// with catalog.response, or search.response when the definition declares no
// separate layout.
func (c *DefinitionConnector) Catalog(ctx context.Context, req CatalogRequest) (CatalogResult, error) {
	if !c.def.HasCatalog() {
		return CatalogResult{}, fmt.Errorf("definition %q: no catalog", c.def.Meta.ID)
	}
	if err := c.ensureLogin(ctx); err != nil {
		return CatalogResult{}, err
	}
	catalog := c.def.Catalog
	forum := strings.TrimSpace(req.Forum)
	if forum == "" {
		return CatalogResult{}, fmt.Errorf("definition %q: catalog page without forum", c.def.Meta.ID)
	}
	standard := 0
	for _, candidate := range catalog.Forums {
		if strings.EqualFold(strings.TrimSpace(candidate.ID), forum) {
			standard = candidate.Standard
			break
		}
	}
	pageSize := req.PageSize
	if pageSize <= 0 {
		pageSize = catalog.PageSize
	}
	if pageSize <= 0 {
		pageSize = 50
	}

	values := c.templateValues(SearchRequest{Mode: ModeSearch}, 1, 0)
	values.scalars["forum"] = forum
	values.scalars["start"] = strconv.Itoa(req.Start)
	values.scalars["page_size"] = strconv.Itoa(pageSize)

	// A fixed page (an index topic) is read as it is, with no forum or start
	// parameter; next, when set, follows its pagination.
	fixedURL := ""
	fixedNext := ""
	for _, candidate := range catalog.Forums {
		if strings.EqualFold(strings.TrimSpace(candidate.ID), forum) ||
			(candidate.ID == "" && strings.EqualFold(strings.TrimSpace(candidate.URL), forum)) {
			fixedURL = strings.TrimSpace(candidate.URL)
			fixedNext = strings.TrimSpace(candidate.Next)
			break
		}
	}
	if fixedURL == "" && strings.TrimSpace(catalog.Request.Get) == "" && strings.TrimSpace(catalog.Request.Post) == "" {
		return CatalogResult{}, fmt.Errorf("definition %q: catalog forum %q has no url", c.def.Meta.ID, forum)
	}

	spec := c.def.Search.Response
	if len(catalog.Response.Fields) > 0 {
		spec = catalog.Response
	}
	result := CatalogResult{PageSize: pageSize}

	if fixedURL != "" {
		address := fixedURL
		for page := 0; page < catalogMaxPages && address != ""; page++ {
			target, err := c.buildURL(address)
			if err != nil {
				return CatalogResult{}, err
			}
			request, err := c.newRequest(ctx, http.MethodGet, target, nil)
			if err != nil {
				return CatalogResult{}, err
			}
			response, err := c.doFeed(ctx, request)
			if err != nil {
				return CatalogResult{}, err
			}
			if response.Status >= 400 {
				return CatalogResult{}, fmt.Errorf("definition %q: catalog returned HTTP %d", c.def.Meta.ID, response.Status)
			}
			pageEntries, err := c.catalogEntries(ctx, response, spec, forum, standard)
			if err != nil {
				return CatalogResult{}, err
			}
			result.Entries = append(result.Entries, pageEntries...)
			address = ""
			if fixedNext != "" {
				address, _ = extractHTMLValue(htmlBody(response), fixedNext, "href")
			}
		}
		result.PageSize = 0
		result.More = false
		return result, nil
	}

	pathTemplate := catalog.Request.Post
	method := http.MethodPost
	if pathTemplate == "" {
		pathTemplate = catalog.Request.Get
		method = http.MethodGet
	}
	if strings.TrimSpace(pathTemplate) == "" {
		return CatalogResult{}, fmt.Errorf("definition %q: catalog.request needs get or post", c.def.Meta.ID)
	}
	renderedPath, err := values.render(pathTemplate)
	if err != nil {
		return CatalogResult{}, fmt.Errorf("definition %q: catalog request: %w", c.def.Meta.ID, err)
	}
	target, err := c.buildURL(renderedPath)
	if err != nil {
		return CatalogResult{}, err
	}
	query := target.Query()
	params, err := values.renderMap(catalog.Request.Params)
	if err != nil {
		return CatalogResult{}, fmt.Errorf("definition %q: catalog params: %w", c.def.Meta.ID, err)
	}
	for name, value := range params {
		query.Set(name, value)
	}
	if name := strings.TrimSpace(catalog.ForumParam); name != "" {
		query.Set(name, forum)
	}
	if name := strings.TrimSpace(catalog.StartParam); name != "" {
		query.Set(name, strconv.Itoa(req.Start))
	}
	target.RawQuery = query.Encode()

	build := func() (*http.Request, error) {
		return c.newRequest(ctx, method, target, nil)
	}
	stamp := c.session.loginStamp()
	request, err := build()
	if err != nil {
		return CatalogResult{}, err
	}
	response, err := c.doFeed(ctx, request)
	if err != nil {
		return CatalogResult{}, err
	}
	// A forum that keeps one session per account answers as a guest once the
	// account logged in elsewhere: 200, the login page, no topics. The missing
	// auth.logged_in marker tells it apart from an empty forum.
	relogged := false
	if response.Status < 400 && c.sessionLost(response) {
		relogged = true
		if err := c.reLoginAfter(ctx, stamp); err != nil {
			return CatalogResult{}, err
		}
		if request, err = build(); err != nil {
			return CatalogResult{}, err
		}
		if response, err = c.doFeed(ctx, request); err != nil {
			return CatalogResult{}, err
		}
	}
	if (response.Status == http.StatusUnauthorized || response.Status == http.StatusForbidden) &&
		c.canReLogin() && !relogged {
		if err := c.forceReLogin(ctx); err != nil {
			return CatalogResult{}, err
		}
		if request, err = build(); err != nil {
			return CatalogResult{}, err
		}
		if response, err = c.doFeed(ctx, request); err != nil {
			return CatalogResult{}, err
		}
	}
	if response.Status >= 400 {
		return CatalogResult{}, fmt.Errorf("definition %q: catalog returned HTTP %d", c.def.Meta.ID, response.Status)
	}

	result.Entries, err = c.catalogEntries(ctx, response, spec, forum, standard)
	if err != nil {
		return CatalogResult{}, err
	}
	result.More = len(result.Entries) > 0
	return result, nil
}

// catalogMaxPages bounds the pages followed from a fixed index topic: the TV
// list of DDUnlimited has three, and a runaway next link must not read the
// whole forum in one run.
const catalogMaxPages = 5

// catalogEntries parses the rows of one catalogue page into entries.
func (c *DefinitionConnector) catalogEntries(ctx context.Context, response *Response, spec definition.Response, forum string, standard int) ([]CatalogEntry, error) {
	rows, err := c.extractRowsWith(response, spec)
	if err != nil {
		return nil, err
	}
	entries := make([]CatalogEntry, 0, len(rows))
	for _, raw := range rows {
		item, ok, err := c.normalizeRowWith(raw, response.URL, spec)
		if err != nil {
			return nil, err
		}
		observeNormalize(ctx, NormalizeObservation{OK: ok})
		if !ok {
			continue
		}
		address := strings.TrimSpace(item.DetailsURL)
		if address == "" {
			address = strings.TrimSpace(item.DownloadURL)
		}
		if address == "" || strings.TrimSpace(item.Title) == "" {
			continue
		}
		absolute := address
		if parsed, parseErr := url.Parse(address); parseErr == nil && !parsed.IsAbs() && c.baseURL != nil {
			absolute = c.baseURL.ResolveReference(parsed).String()
		}
		entries = append(entries, CatalogEntry{
			Title:    item.Title,
			URL:      absolute,
			Forum:    forum,
			Standard: standard,
		})
	}
	return entries, nil
}

// CatalogForums returns the forum ids of the definition's topic history.
func (c *DefinitionConnector) CatalogForums() []definition.CatalogForum {
	if !c.def.HasCatalog() {
		return nil
	}
	return c.def.Catalog.Forums
}
