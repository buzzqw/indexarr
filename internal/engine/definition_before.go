package engine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/buzzqw/indexarr/internal/definition"
)

// downloadWithBefore implements download.before: it opens the topic, checks
// whether the magnets are already visible, performs the declared action when
// they are not, and reopens the page. It is reached only from Download, never
// from Search, so a normal query cannot thank a topic.
func (c *DefinitionConnector) downloadWithBefore(ctx context.Context, item Item) (Payload, bool, error) {
	before := c.def.Download.Before
	if before == nil {
		return Payload{}, false, nil
	}
	rawURL := resolveURL(item.DetailsURL, c.baseURL)
	if rawURL == "" {
		return Payload{}, true, errors.New("download: before needs a details URL")
	}
	target, err := url.Parse(rawURL)
	if err != nil {
		return Payload{}, true, errors.New("download: invalid details URL")
	}

	season, episode := c.requestedSeasonEpisode(item)
	first, err := c.fetchDetailsPage(ctx, target)
	if err != nil {
		return Payload{}, true, err
	}
	candidates, err := c.extractDownloadCandidates(first)
	if err != nil {
		return Payload{}, true, err
	}
	if len(candidates) > 0 {
		return c.payloadFromCandidates(ctx, item, candidates, season, episode)
	}

	values := c.downloadTemplateValues(item)
	for name, extractor := range before.Extract {
		value, extractErr := extractAuthValue(first.Body, extractor)
		if extractErr != nil {
			return Payload{}, true, fmt.Errorf("download: before extract %q: %v", name, extractErr)
		}
		values.extracted[name] = value
	}

	if err := c.runBeforeAction(ctx, before, values); err != nil {
		return Payload{}, true, err
	}
	if wait := before.Wait.Duration(); wait > 0 {
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return Payload{}, true, ctx.Err()
		case <-timer.C:
		}
	}

	second, err := c.fetchDetailsPage(ctx, target)
	if err != nil {
		return Payload{}, true, err
	}
	candidates, err = c.extractDownloadCandidates(second)
	if err != nil {
		return Payload{}, true, err
	}
	if len(candidates) == 0 {
		return Payload{}, true, errors.New("download: before action did not reveal a magnet")
	}
	return c.payloadFromCandidates(ctx, item, candidates, season, episode)
}

func (c *DefinitionConnector) runBeforeAction(ctx context.Context, before *definition.DownloadBefore, values *templateValues) error {
	action := before.Action
	pathTemplate := action.Post
	method := http.MethodPost
	if pathTemplate == "" {
		pathTemplate = action.Get
		method = http.MethodGet
	}
	renderedPath, err := values.render(pathTemplate)
	if err != nil {
		return fmt.Errorf("download: before action: %w", err)
	}
	if strings.TrimSpace(renderedPath) == "" {
		return errors.New("download: before action rendered an empty URL")
	}
	target, err := c.buildURL(renderedPath)
	if err != nil {
		return errors.New("download: before action has an invalid URL")
	}

	params, err := values.renderMap(action.Params)
	if err != nil {
		return fmt.Errorf("download: before action params: %w", err)
	}
	query := target.Query()
	for name, value := range params {
		query.Set(name, value)
	}
	target.RawQuery = query.Encode()

	var reader io.Reader
	if method == http.MethodPost {
		form, err := values.renderMap(action.Form)
		if err != nil {
			return fmt.Errorf("download: before action form: %w", err)
		}
		encoded := url.Values{}
		for name, value := range form {
			encoded.Set(name, value)
		}
		reader = strings.NewReader(encoded.Encode())
	}

	request, err := http.NewRequestWithContext(ctx, method, target.String(), reader)
	if err != nil {
		return errors.New("download: build before action request")
	}
	c.applyAuth(request)
	if method == http.MethodPost && request.Header.Get("Content-Type") == "" {
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	headers, err := values.renderMap(action.Headers)
	if err != nil {
		return fmt.Errorf("download: before action headers: %w", err)
	}
	for name, value := range headers {
		request.Header.Set(name, value)
	}

	response, err := c.do(ctx, request)
	if err != nil {
		return c.requestError(err)
	}
	if response.Status < 200 || response.Status >= 400 {
		return fmt.Errorf("download: before action returned HTTP %d", response.Status)
	}
	return nil
}

func (c *DefinitionConnector) payloadFromCandidates(ctx context.Context, item Item, candidates []resolvedDownload, season, episode int) (Payload, bool, error) {
	chosen, ok := c.chooseCandidate(item.Title, candidates, season, episode)
	if !ok {
		return Payload{}, true, errors.New("download: no magnet matches the requested season and episode")
	}
	if chosen.Magnet != "" {
		return Payload{Magnet: chosen.Magnet}, true, nil
	}
	if chosen.DownloadURL != "" {
		payload, err := c.downloadURL(ctx, chosen.DownloadURL, item.Title)
		return payload, true, err
	}
	return Payload{}, true, errors.New("download: no usable download target")
}

func (c *DefinitionConnector) downloadTemplateValues(item Item) *templateValues {
	values := c.authTemplateValues()
	season, episode := c.requestedSeasonEpisode(item)
	values.scalars["item.title"] = item.Title
	values.scalars["item.guid"] = item.GUID
	values.scalars["item.details"] = item.DetailsURL
	values.scalars["item.download"] = item.DownloadURL
	if season > 0 {
		values.scalars["item.season"] = strconv.Itoa(season)
	}
	if episode > 0 {
		values.scalars["item.episode"] = strconv.Itoa(episode)
	}
	return values
}

// requestedSeasonEpisode reads the season/episode carried by the grab request.
// The search puts them in Item.Attributes; a definition may also repeat them
// in the details URL. Both are additive and optional.
func (c *DefinitionConnector) requestedSeasonEpisode(item Item) (int, int) {
	season := attributeInt(item, "season")
	episode := attributeInt(item, "episode")
	if episode == 0 {
		episode = attributeInt(item, "ep")
	}
	if season == 0 || episode == 0 {
		if parsed, err := url.Parse(strings.TrimSpace(item.DetailsURL)); err == nil {
			query := parsed.Query()
			if season == 0 {
				season = atoiPositive(query.Get("season"))
			}
			if episode == 0 {
				episode = atoiPositive(query.Get("ep"))
				if episode == 0 {
					episode = atoiPositive(query.Get("episode"))
				}
			}
		}
	}
	return season, episode
}

func attributeInt(item Item, name string) int {
	if item.Attributes == nil {
		return 0
	}
	return atoiPositive(item.Attributes[name])
}

func atoiPositive(raw string) int {
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || value <= 0 {
		return 0
	}
	return value
}

// chooseCandidate applies Download.Select. Without select, or without a
// requested season/episode, the first candidate wins exactly as before.
func (c *DefinitionConnector) chooseCandidate(title string, candidates []resolvedDownload, season, episode int) (resolvedDownload, bool) {
	if len(candidates) == 0 {
		return resolvedDownload{}, false
	}
	if !strings.EqualFold(strings.TrimSpace(c.def.Download.Select), "season_episode") ||
		(season <= 0 && episode <= 0) {
		return candidates[0], true
	}

	best := -1
	bestScore := 1 << 30
	for i, candidate := range candidates {
		match := parseReleaseName(candidateName(candidate, title))
		if !match.valid || (season > 0 && match.season != season) {
			continue
		}
		if episode > 0 {
			if match.episodeStart == 0 {
				continue
			}
			if match.episodeEnd > 0 {
				if episode < match.episodeStart || episode > match.episodeEnd {
					continue
				}
			} else if episode != match.episodeStart {
				continue
			}
			// Prefer the narrowest range: a single episode beats a pack.
			span := match.episodeEnd - match.episodeStart
			if span < 0 {
				span = 0
			}
			if span < bestScore {
				best = i
				bestScore = span
			}
			continue
		}
		// Season only: prefer a full-season pack, then any release of it.
		score := 1
		if match.episodeStart == 0 {
			score = 0
		}
		if score < bestScore {
			best = i
			bestScore = score
		}
	}
	if best < 0 {
		return resolvedDownload{}, false
	}
	return candidates[best], true
}

func candidateName(candidate resolvedDownload, fallback string) string {
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(candidate.Magnet)), "magnet:") {
		if parsed, err := url.Parse(candidate.Magnet); err == nil {
			if name := strings.TrimSpace(parsed.Query().Get("dn")); name != "" {
				return name
			}
		}
	}
	if candidate.DownloadURL != "" {
		if parsed, err := url.Parse(candidate.DownloadURL); err == nil {
			if name := strings.TrimSpace(parsed.Query().Get("dn")); name != "" {
				return name
			}
			if base := strings.TrimSpace(pathBase(parsed.Path)); base != "" {
				return base
			}
		}
	}
	return fallback
}

func pathBase(path string) string {
	path = strings.TrimRight(path, "/")
	if index := strings.LastIndex(path, "/"); index >= 0 {
		return path[index+1:]
	}
	return path
}

type releaseNameMatch struct {
	valid        bool
	season       int
	episodeStart int
	episodeEnd   int
}

var (
	releaseSxxEyy = regexp.MustCompile(`(?i)\bS(\d{1,2})[ ._-]?E(\d{1,3})(?:[ ._-]?E?(\d{1,3}))?`)
	releaseNxMM   = regexp.MustCompile(`(?i)\b(\d{1,2})x(\d{2,3})(?:[ ._-]?-?\s*(\d{1,3}))?`)
	releaseSeason = regexp.MustCompile(`(?i)\b(?:stagione|stagioni|season|st\.?)\s*(\d{1,2})(?:[^\d]{0,16}?(?:ep\.?|e)\s*(\d{1,3})(?:\s*-\s*(\d{1,3}))?)?`)
	releaseSPack  = regexp.MustCompile(`(?i)\bS(\d{1,2})[ ._-]?(?:-|–)\s*S?(\d{1,2})\b`)
	releaseSOnly  = regexp.MustCompile(`(?i)\bS(\d{1,2})\b`)
)

// parseReleaseName extracts the season and episode range from a magnet display
// name or a file name. It intentionally mirrors the rules of the Python
// service only where the magnet choice needs them.
func parseReleaseName(name string) releaseNameMatch {
	name = strings.TrimSpace(name)
	if name == "" {
		return releaseNameMatch{}
	}
	if match := releaseSxxEyy.FindStringSubmatch(name); match != nil {
		return releaseNameMatch{
			valid:        true,
			season:       atoiPositive(match[1]),
			episodeStart: atoiPositive(match[2]),
			episodeEnd:   atoiPositive(match[3]),
		}
	}
	if match := releaseNxMM.FindStringSubmatch(name); match != nil {
		return releaseNameMatch{
			valid:        true,
			season:       atoiPositive(match[1]),
			episodeStart: atoiPositive(match[2]),
			episodeEnd:   atoiPositive(match[3]),
		}
	}
	if match := releaseSeason.FindStringSubmatch(name); match != nil {
		return releaseNameMatch{
			valid:        true,
			season:       atoiPositive(match[1]),
			episodeStart: atoiPositive(match[2]),
			episodeEnd:   atoiPositive(match[3]),
		}
	}
	if match := releaseSPack.FindStringSubmatch(name); match != nil {
		return releaseNameMatch{
			valid:  true,
			season: atoiPositive(match[1]),
		}
	}
	if match := releaseSOnly.FindStringSubmatch(name); match != nil {
		return releaseNameMatch{
			valid:  true,
			season: atoiPositive(match[1]),
		}
	}
	return releaseNameMatch{}
}
