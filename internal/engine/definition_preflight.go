package engine

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// runSearchPreflight executes the optional pre-search request and stores the
// extracted values in the connector session. It is used by sites that require
// a CSRF token generated on the page before the search form can be submitted.
func (c *DefinitionConnector) runSearchPreflight(ctx context.Context, req SearchRequest) error {
	pre := c.def.Search.Preflight
	if pre == nil {
		return nil
	}
	values := c.templateValues(req, 1, 0)

	pathTemplate := pre.Post
	method := http.MethodPost
	if pathTemplate == "" {
		pathTemplate = pre.Get
		method = http.MethodGet
	}
	if strings.TrimSpace(pathTemplate) == "" {
		return fmt.Errorf("definition %q: search.preflight needs get or post", c.def.Meta.ID)
	}
	renderedPath, err := values.render(pathTemplate)
	if err != nil {
		return fmt.Errorf("definition %q: search.preflight: %w", c.def.Meta.ID, err)
	}
	target, err := c.buildURL(renderedPath)
	if err != nil {
		return err
	}

	query := target.Query()
	params, err := values.renderMap(pre.Params)
	if err != nil {
		return fmt.Errorf("definition %q: search.preflight params: %w", c.def.Meta.ID, err)
	}
	for name, value := range params {
		query.Set(name, value)
	}
	target.RawQuery = query.Encode()

	var body *strings.Reader
	if method == http.MethodPost {
		form, err := values.renderMap(pre.Form)
		if err != nil {
			return fmt.Errorf("definition %q: search.preflight form: %w", c.def.Meta.ID, err)
		}
		encoded := url.Values{}
		for name, value := range form {
			encoded.Set(name, value)
		}
		body = strings.NewReader(encoded.Encode())
	}

	var request *http.Request
	if body == nil {
		request, err = http.NewRequestWithContext(ctx, method, target.String(), nil)
	} else {
		request, err = http.NewRequestWithContext(ctx, method, target.String(), body)
	}
	if err != nil {
		return fmt.Errorf("definition %q: build search.preflight request", c.def.Meta.ID)
	}
	c.applyAuth(request)
	if method == http.MethodPost && request.Header.Get("Content-Type") == "" {
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	headers, err := values.renderMap(pre.Headers)
	if err != nil {
		return fmt.Errorf("definition %q: search.preflight headers: %w", c.def.Meta.ID, err)
	}
	for name, value := range headers {
		request.Header.Set(name, value)
	}

	response, err := c.do(ctx, request)
	if err != nil {
		return err
	}
	if response.Status < 200 || response.Status >= 400 {
		return fmt.Errorf("definition %q: search.preflight returned HTTP %d", c.def.Meta.ID, response.Status)
	}
	for name, extractor := range pre.Extract {
		value, err := extractAuthValue(response.Body, extractor)
		if err != nil {
			return fmt.Errorf("definition %q: search.preflight extract %s: %w", c.def.Meta.ID, name, err)
		}
		value = strings.TrimSpace(value)
		if value == "" {
			return fmt.Errorf("definition %q: search.preflight extract %s: empty value", c.def.Meta.ID, name)
		}
		c.session.SetValue(name, value)
	}
	return nil
}
