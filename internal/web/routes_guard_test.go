package web

import (
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// outerPrefixes are the paths Handler serves outside the UI mux.
var outerPrefixes = []string{"/api", "/newznab/api", "/torznab/", "/dl/", "/pair", "/healthz", "/metrics", "/sw.js", "/static/"}

// templateLink matches the literal start of a link or a form target in a
// template: href="/…", action="/…", formaction="/…".
var templateLink = regexp.MustCompile(`(?:href|action|formaction)="(/[^"]*)"`)

// templateAction drops the template actions inside a link, so that
// /indexers/{{.ID}}/edit is checked as /indexers/1/edit.
var templateAction = regexp.MustCompile(`\{\{[^}]*\}\}|%[sdv]`)

// TestUILinksAndFormsReachARoute fails when a template or a handler points a
// link or a form at a path no route serves. A route removed by mistake leaves
// a button that answers 404: this happened to the confirmation of the fourth
// stored source.
func TestUILinksAndFormsReachARoute(t *testing.T) {
	mux := (&Server{}).uiRoutes()
	firstSegments := map[string]bool{}
	// The first path segments of the UI routes decide which Go string
	// literals are links: "/indexers/" is, "/etc/ssl" is not.
	for _, path := range routeProbePaths() {
		if routeResolves(mux, path) {
			firstSegments[firstSegment(path)] = true
		}
	}

	var missing []string
	check := func(where, raw string) {
		path := normalizeLinkPath(raw)
		if path == "" || isOuterPath(path) || routeResolves(mux, path) {
			return
		}
		missing = append(missing, where+": "+raw)
	}

	templates, err := filepath.Glob("templates/*.html")
	if err != nil || len(templates) == 0 {
		t.Fatalf("no templates found: %v", err)
	}
	for _, file := range templates {
		body, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, match := range templateLink.FindAllStringSubmatch(string(body), -1) {
			check(file, match[1])
		}
	}
	for _, path := range uiPathsFromGo(t, firstSegments) {
		check(path.where, path.value)
	}

	if len(missing) > 0 {
		sort.Strings(missing)
		t.Errorf("links and form targets without a route:\n  %s", strings.Join(missing, "\n  "))
	}
}

// routeProbePaths lists one concrete path per first segment worth probing,
// enough to learn which segments belong to the UI.
func routeProbePaths() []string {
	return []string{
		"/setup", "/language/en", "/releases", "/sources", "/definitions", "/indexers",
		"/applications", "/activity", "/runs", "/rules", "/logs", "/proxies",
		"/settings", "/download-clients", "/maintenance",
	}
}

type goPath struct {
	where string
	value string
}

// uiPathsFromGo returns the string literals of the package's non-test Go
// files whose first segment is a UI segment.
func uiPathsFromGo(t *testing.T, segments map[string]bool) []goPath {
	t.Helper()
	if segments == nil {
		return nil
	}
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	var out []goPath
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		parsed, err := parser.ParseFile(fset, file, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		var stack []ast.Node
		ast.Inspect(parsed, func(node ast.Node) bool {
			if node == nil {
				stack = stack[:len(stack)-1]
				return true
			}
			parent := ast.Node(nil)
			if len(stack) > 0 {
				parent = stack[len(stack)-1]
			}
			stack = append(stack, node)
			literal, ok := node.(*ast.BasicLit)
			if !ok || literal.Kind != token.STRING || isPathComparison(parent) {
				return true
			}
			value, err := strconv.Unquote(literal.Value)
			if err != nil || !strings.HasPrefix(value, "/") || strings.ContainsAny(value, " \n") {
				return true
			}
			if !segments[firstSegment(value)] {
				return true
			}
			out = append(out, goPath{where: fset.Position(literal.Pos()).String(), value: value})
			return true
		})
	}
	return out
}

// isPathComparison reports whether a literal is compared with a request path
// (path == "/x", strings.HasPrefix(path, "/x/")) rather than linked to.
func isPathComparison(parent ast.Node) bool {
	switch node := parent.(type) {
	case *ast.BinaryExpr:
		return node.Op == token.EQL || node.Op == token.NEQ
	case *ast.CallExpr:
		selector, ok := node.Fun.(*ast.SelectorExpr)
		return ok && (selector.Sel.Name == "HasPrefix" || selector.Sel.Name == "HasSuffix" || selector.Sel.Name == "TrimPrefix")
	case *ast.CaseClause:
		return true
	}
	return false
}

func firstSegment(path string) string {
	path = strings.TrimPrefix(path, "/")
	if index := strings.IndexAny(path, "/?#"); index >= 0 {
		path = path[:index]
	}
	return path
}

// normalizeLinkPath turns a link as written into a path the mux can resolve:
// template actions and an open trailing segment ("/indexers/"+id) become 1,
// fmt verbs (%d, %s) become 1 as well; the query and the fragment go away. External and empty links return "".
func normalizeLinkPath(raw string) string {
	if strings.HasPrefix(raw, "//") {
		return ""
	}
	path := templateAction.ReplaceAllString(raw, "1")
	if index := strings.IndexAny(path, "?#"); index >= 0 {
		path = path[:index]
	}
	if path == "" {
		return ""
	}
	if path != "/" && strings.HasSuffix(path, "/") {
		path += "1"
	}
	return path
}

func isOuterPath(path string) bool {
	for _, prefix := range outerPrefixes {
		if path == strings.TrimSuffix(prefix, "/") || strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return false
}

func routeResolves(mux *http.ServeMux, path string) bool {
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		if _, pattern := mux.Handler(httptest.NewRequest(method, path, nil)); pattern != "" {
			return true
		}
	}
	return false
}

func TestRedirectPutsTheMessageBeforeTheFragment(t *testing.T) {
	cases := []struct{ path, encoded, want string }{
		{"/applications#clients", "msg=ok", "/applications?msg=ok#clients"},
		{"/settings?section=advanced#definitions", "err=x", "/settings?section=advanced&err=x#definitions"},
		{"/indexers", "msg=ok", "/indexers?msg=ok"},
		{"/indexers#top", "", "/indexers#top"},
	}
	for _, tc := range cases {
		if got := withQuery(tc.path, tc.encoded); got != tc.want {
			t.Errorf("withQuery(%q, %q) = %q, want %q", tc.path, tc.encoded, got, tc.want)
		}
	}
}

func TestOldSettingsPagesKeepTheMessage(t *testing.T) {
	env := newTestEnv(t)
	for path, want := range map[string]string{
		"/definitions?msg=done": "/settings?msg=done&section=advanced#definitions",
		"/rules?err=bad":        "/settings?err=bad&section=rules",
		"/maintenance":          "/settings?section=maintenance",
	} {
		recorder := get(t, env.handler, path)
		if got := recorder.Header().Get("Location"); got != want {
			t.Errorf("GET %s -> %q, want %q", path, got, want)
		}
	}
}
