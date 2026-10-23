package cardigann

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/buzzqw/indexarr/internal/definition"
)

// Cardigann filters are translated to native v1 filters as follows:
//
//	trim                 -> trim
//	tolower              -> lower
//	toupper              -> upper
//	replace [old, new]   -> replace with arg "old:new"
//	append [suffix]      -> suffix
//	prepend [prefix]     -> prefix
//	querystring [param]  -> query_param
//	regexp [pattern]     -> regex (only when there is no replacement)
//	dateparse [layouts]  -> field layouts; field type becomes date
//	timeparse [layouts]  -> field layouts; a trailing timezone is dropped
//	htmldecode           -> dropped (native HTML extraction decodes entities)
//
// split, join and urldecode have no native v1 equivalent and are reported as
// unsupported. An unknown filter is reported too; it is never emitted, so a
// converted definition cannot contain a filter the engine would reject at
// runtime.

type filterTranslation struct {
	filters   []definition.Filter
	layouts   []string
	inferDate bool
}

func (c *converter) convertFilters(field string, raws rawFilters) filterTranslation {
	var out filterTranslation
	for _, raw := range raws {
		if raw.Invalid || strings.TrimSpace(raw.Name) == "" {
			c.addUnsupported(fmt.Sprintf("filter in field %q", field))
			c.addWarning("field %q has a filter with an unrecognised shape; dropped", field)
			continue
		}
		name := strings.ToLower(strings.TrimSpace(raw.Name))
		args := raw.Args
		switch name {
		case "trim":
			out.filters = append(out.filters, definition.Filter{Name: "trim"})
		case "tolower", "lower", "lowercase":
			out.filters = append(out.filters, definition.Filter{Name: "lower"})
		case "toupper", "upper", "uppercase":
			out.filters = append(out.filters, definition.Filter{Name: "upper"})
		case "replace":
			if len(args) < 2 {
				c.unsupportedFilter(field, name, "needs two arguments")
				continue
			}
			if len(args) > 2 {
				c.addWarning("field %q replace filter has extra arguments; ignored", field)
			}
			old, replacement := args[0], args[1]
			if old == "" {
				c.unsupportedFilter(field, name, "has an empty search string")
				continue
			}
			if strings.Contains(old, ":") {
				c.unsupportedFilter(field, name, "cannot encode a colon in the search string")
				continue
			}
			out.filters = append(out.filters, definition.Filter{
				Name: "replace",
				Arg:  old + ":" + replacement,
			})
		case "append", "suffix":
			if len(args) == 0 {
				c.unsupportedFilter(field, name, "needs an argument")
				continue
			}
			out.filters = append(out.filters, definition.Filter{Name: "suffix", Arg: args[0]})
		case "prepend", "prefix":
			if len(args) == 0 {
				c.unsupportedFilter(field, name, "needs an argument")
				continue
			}
			out.filters = append(out.filters, definition.Filter{Name: "prefix", Arg: args[0]})
		case "dateparse":
			added := false
			for _, layout := range args {
				if strings.TrimSpace(layout) == "" {
					continue
				}
				out.layouts = append(out.layouts, layout)
				added = true
			}
			if !added {
				c.unsupportedFilter(field, name, "has no layout")
				continue
			}
			out.inferDate = true
		case "timeparse":
			for i, layout := range args {
				if strings.TrimSpace(layout) == "" {
					continue
				}
				// The second argument may be a time zone name. Native date
				// parsing has no per-field zone override; the layout is kept.
				if i == 1 && !strings.ContainsAny(layout, "0123456789") {
					c.addWarning("field %q timeparse timezone %q is not represented; ignored", field, layout)
					continue
				}
				out.layouts = append(out.layouts, layout)
			}
			if len(out.layouts) == 0 {
				c.unsupportedFilter(field, name, "has no layout")
				continue
			}
			out.inferDate = true
		case "regexp":
			if len(args) == 0 || strings.TrimSpace(args[0]) == "" {
				c.unsupportedFilter(field, name, "has no pattern")
				continue
			}
			if len(args) > 1 && args[1] != "" {
				c.unsupportedFilter(field, name, "uses a replacement, which native regex cannot express")
				continue
			}
			if _, err := regexp.Compile(args[0]); err != nil {
				c.unsupportedFilter(field, name, "has an invalid pattern")
				continue
			}
			out.filters = append(out.filters, definition.Filter{Name: "regex", Arg: args[0]})
		case "querystring", "queryparam":
			if len(args) == 0 || strings.TrimSpace(args[0]) == "" {
				c.unsupportedFilter(field, name, "needs a parameter name")
				continue
			}
			out.filters = append(out.filters, definition.Filter{Name: "query_param", Arg: args[0]})
		case "htmldecode":
			// HTML parsers already decode entities; keeping the filter would
			// make the native engine reject the definition.
			c.addWarning("field %q htmldecode filter is implicit in native extraction; dropped", field)
		case "split":
			c.unsupportedFilter(field, name, "has no native equivalent")
		case "join":
			c.unsupportedFilter(field, name, "has no native equivalent")
		case "urldecode":
			c.unsupportedFilter(field, name, "has no native equivalent")
		default:
			c.addUnsupported(fmt.Sprintf("filter %q", name))
			c.addWarning("field %q uses unsupported filter %q; dropped", field, name)
		}
	}
	return out
}

func (c *converter) unsupportedFilter(field, name, reason string) {
	c.addUnsupported(fmt.Sprintf("filter %q", name))
	c.addWarning("field %q filter %q %s; dropped", field, name, reason)
}
