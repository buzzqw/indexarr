package engine

import (
	"bytes"
	"mime"
	"regexp"
	"strings"
	"unicode/utf8"
)

// metaCharsetPattern finds <meta charset="x"> and the http-equiv form
// (content="text/html; charset=x") near the top of a page.
var metaCharsetPattern = regexp.MustCompile(`(?i)<meta[^>]+charset\s*=\s*["']?\s*([a-z0-9_-]+)`)

// htmlBody returns the response body as UTF-8 for the HTML extractors. Many
// Russian trackers still serve windows-1251 and some western sites Latin-1:
// without this step every Cyrillic title would reach the apps as mojibake.
// The charset comes from the Content-Type header, then from a <meta> tag in
// the first 2 KiB. A body that is already valid UTF-8 is returned unchanged,
// so a page re-encoded by FlareSolverr (which keeps the original header) is
// never decoded twice.
func htmlBody(response *Response) []byte {
	if response == nil {
		return nil
	}
	body := response.Body
	if utf8.Valid(body) {
		return body
	}
	charset := ""
	if response.Header != nil {
		if _, params, err := mime.ParseMediaType(response.Header.Get("Content-Type")); err == nil {
			charset = params["charset"]
		}
	}
	if charset == "" {
		head := body
		if len(head) > 2048 {
			head = head[:2048]
		}
		if match := metaCharsetPattern.FindSubmatch(head); match != nil {
			charset = string(match[1])
		}
	}
	switch strings.ToLower(strings.TrimSpace(charset)) {
	case "windows-1251", "cp1251", "win-1251", "x-cp1251":
		return cp1251ToUTF8(body)
	case "iso-8859-1", "iso8859-1", "latin1", "latin-1", "windows-1252", "cp1252":
		return latin1ToUTF8(body)
	default:
		return body
	}
}

// cp1251High maps the upper half (0x80–0xFF) of the public Windows-1251 code
// page. 0x98 is undefined in the code page and decodes to U+FFFD.
var cp1251High = [128]rune{
	'Ђ', 'Ѓ', '‚', 'ѓ', '„', '…', '†', '‡',
	'€', '‰', 'Љ', '‹', 'Њ', 'Ќ', 'Ћ', 'Џ',
	'ђ', '‘', '’', '“', '”', '•', '–', '—',
	'�', '™', 'љ', '›', 'њ', 'ќ', 'ћ', 'џ',
	' ', 'Ў', 'ў', 'Ј', '¤', 'Ґ', '¦', '§',
	'Ё', '©', 'Є', '«', '¬', '­', '®', 'Ї',
	'°', '±', 'І', 'і', 'ґ', 'µ', '¶', '·',
	'ё', '№', 'є', '»', 'ј', 'Ѕ', 'ѕ', 'ї',
	// 0xC0–0xFF: А–я in order.
	'А', 'Б', 'В', 'Г', 'Д', 'Е', 'Ж', 'З',
	'И', 'Й', 'К', 'Л', 'М', 'Н', 'О', 'П',
	'Р', 'С', 'Т', 'У', 'Ф', 'Х', 'Ц', 'Ч',
	'Ш', 'Щ', 'Ъ', 'Ы', 'Ь', 'Э', 'Ю', 'Я',
	'а', 'б', 'в', 'г', 'д', 'е', 'ж', 'з',
	'и', 'й', 'к', 'л', 'м', 'н', 'о', 'п',
	'р', 'с', 'т', 'у', 'ф', 'х', 'ц', 'ч',
	'ш', 'щ', 'ъ', 'ы', 'ь', 'э', 'ю', 'я',
}

func cp1251ToUTF8(raw []byte) []byte {
	var out bytes.Buffer
	out.Grow(len(raw) + len(raw)/2)
	for _, b := range raw {
		if b < 0x80 {
			out.WriteByte(b)
			continue
		}
		out.WriteRune(cp1251High[b-0x80])
	}
	return out.Bytes()
}
