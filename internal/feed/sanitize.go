package feed

import (
	"bytes"
	"strings"
)

// namedEntities maps the HTML entities that scraped feeds commonly contain to
// their UTF-8 value. XML itself only defines amp, lt, gt, quot and apos.
var namedEntities = map[string]string{
	"amp":    "&amp;",
	"lt":     "&lt;",
	"gt":     "&gt;",
	"quot":   "&quot;",
	"apos":   "&apos;",
	"nbsp":   " ",
	"ndash":  "–",
	"mdash":  "—",
	"hellip": "…",
	"lsquo":  "‘",
	"rsquo":  "’",
	"ldquo":  "“",
	"rdquo":  "”",
	"laquo":  "«",
	"raquo":  "»",
	"deg":    "°",
	"euro":   "€",
	"middot": "·",
}

// sanitizeXML makes a feed parseable when an indexer scrapes text that is not
// valid XML: a raw & or a named HTML entity made the whole answer unreadable.
// The function escapes unknown entities and raw ampersands, maps common HTML
// entities and drops control characters XML forbids.
func sanitizeXML(data []byte) []byte {
	var out bytes.Buffer
	out.Grow(len(data))
	for i := 0; i < len(data); {
		c := data[i]
		if c < 0x20 && c != '\t' && c != '\n' && c != '\r' {
			i++
			continue
		}
		if c != '&' {
			out.WriteByte(c)
			i++
			continue
		}
		end := bytes.IndexByte(data[i:], ';')
		if end > 0 && end <= 12 {
			entity := string(data[i+1 : i+end])
			if replacement, ok := namedEntities[strings.ToLower(entity)]; ok {
				out.WriteString(replacement)
				i += end + 1
				continue
			}
			if isCharRef(entity) {
				out.Write(data[i : i+end+1])
				i += end + 1
				continue
			}
		}
		out.WriteString("&amp;")
		i++
	}
	return out.Bytes()
}

func isCharRef(entity string) bool {
	if !strings.HasPrefix(entity, "#") {
		return false
	}
	digits := entity[1:]
	base := 10
	if strings.HasPrefix(digits, "x") || strings.HasPrefix(digits, "X") {
		base = 16
		digits = digits[1:]
	}
	if digits == "" {
		return false
	}
	for _, r := range digits {
		switch base {
		case 10:
			if r < '0' || r > '9' {
				return false
			}
		case 16:
			if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')) {
				return false
			}
		}
	}
	return true
}
