package engine

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// ed2kLink is a parsed `ed2k://|file|name|size|hash|/` link. The engine only
// needs the parts it archives and shows: the file name, the byte size and the
// MD4 hash that identifies the file on the eD2k network.
type ed2kLink struct {
	Name string
	Size int64
	Hash string // lowercase MD4, 32 hexadecimal characters
}

// parseEd2kFileLink accepts an eD2k file link. The trailing part after the
// hash is tolerated (eMule writes `|/`), and the file name is percent-decoded
// when it contains escapes. A literal `+` stays a plus: eMule links encode
// spaces as `%20`.
func parseEd2kFileLink(raw string) (ed2kLink, bool) {
	raw = strings.TrimSpace(raw)
	if !strings.HasPrefix(strings.ToLower(raw), "ed2k://|file|") {
		return ed2kLink{}, false
	}
	rest := raw[len("ed2k://|file|"):]
	parts := strings.Split(rest, "|")
	if len(parts) < 3 {
		return ed2kLink{}, false
	}
	name, err := url.PathUnescape(parts[0])
	if err != nil {
		return ed2kLink{}, false
	}
	name = strings.TrimSpace(name)
	if name == "" || strings.ContainsAny(name, "\x00\r\n") {
		return ed2kLink{}, false
	}
	size, err := strconv.ParseInt(strings.TrimSpace(parts[1]), 10, 64)
	if err != nil || size <= 0 {
		return ed2kLink{}, false
	}
	hash := strings.ToLower(strings.TrimSpace(parts[2]))
	if !validEd2kHash(hash) {
		return ed2kLink{}, false
	}
	return ed2kLink{Name: name, Size: size, Hash: hash}, true
}

// validEd2kHash reports whether value is a 32-character hexadecimal MD4 hash.
func validEd2kHash(value string) bool {
	if len(value) != 32 {
		return false
	}
	for _, r := range value {
		switch {
		case r >= '0' && r <= '9':
		case r >= 'a' && r <= 'f':
		case r >= 'A' && r <= 'F':
		default:
			return false
		}
	}
	return true
}

// canonicalEd2kLink renders the link with an explicit size and a lowercase
// hash, so the same file always archives with the same string.
func canonicalEd2kLink(link ed2kLink) string {
	name := url.PathEscape(link.Name)
	name = strings.ReplaceAll(name, "+", "%2B")
	return fmt.Sprintf("ed2k://|file|%s|%d|%s|/", name, link.Size, strings.ToLower(link.Hash))
}

// ed2kLinksInText returns every ed2k:// file URI contained in a text. It
// accepts the two shapes found on phpBB topics: an href attribute and a
// JavaScript `filearray` entry. The URI stops at a quote, a whitespace or an
// angle bracket, so the trailing `;` of a JS statement is not included.
func ed2kLinksInText(text string) []string {
	const prefix = "ed2k://|file|"
	lower := strings.ToLower(text)
	var out []string
	for offset := 0; offset < len(text); {
		index := strings.Index(lower[offset:], prefix)
		if index < 0 {
			break
		}
		start := offset + index
		end := start + len(prefix)
		for end < len(text) {
			switch text[end] {
			case '"', '\'', '<', '>', ' ', '\t', '\r', '\n':
				goto found
			}
			end++
		}
	found:
		out = append(out, text[start:end])
		offset = end
	}
	return out
}
