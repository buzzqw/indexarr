package textutil

// A site that serves a page in UTF-8 but decodes it as Latin-1 or CP1252
// before printing the title (LimeTorrents does it, for example) hands out a
// text where every original byte is a character of its own: «Большой Бак»
// arrives as «Ð‘Ð¾Ð»ÑŒÑˆÐ¾Ð¹». The bytes are all there, only read with the wrong
// charset, so the repair is to write them back and read them again as UTF-8.
//
// The repair is deliberately conservative: it applies only when every
// character of the text fits the CP1252/Latin-1 range (so correct Cyrillic,
// Greek or CJK text — characters above U+00FF — is never touched), when the
// bytes it produces are valid UTF-8, and when the result carries no C1
// control characters, which are what a wrong charset leaves behind. A title
// that is already correct comes back unchanged.

import (
	"strings"
	"unicode/utf8"
)

// cp1252Bytes holds the bytes 0x80–0x9F whose CP1252 character is not the
// byte itself. The remaining bytes of that range decode to the C1 controls,
// which are handled like Latin-1.
var cp1252Bytes = map[rune]byte{
	'\u20AC': 0x80, // €
	'\u201A': 0x82, // ‚
	'\u0192': 0x83, // ƒ
	'\u201E': 0x84, // „
	'\u2026': 0x85, // …
	'\u2020': 0x86, // †
	'\u2021': 0x87, // ‡
	'\u02C6': 0x88, // ˆ
	'\u2030': 0x89, // ‰
	'\u0160': 0x8A, // Š
	'\u2039': 0x8B, // ‹
	'\u0152': 0x8C, // Œ
	'\u017D': 0x8E, // Ž
	'\u2018': 0x91, // ‘
	'\u2019': 0x92, // ’
	'\u201C': 0x93, // “
	'\u201D': 0x94, // ”
	'\u2022': 0x95, // •
	'\u2013': 0x96, // –
	'\u2014': 0x97, // —
	'\u02DC': 0x98, // ˜
	'\u2122': 0x99, // ™
	'\u0161': 0x9A, // š
	'\u203A': 0x9B, // ›
	'\u0153': 0x9C, // œ
	'\u017E': 0x9E, // ž
	'\u0178': 0x9F, // Ÿ
}

// RepairDoubleEncoded returns text with a UTF-8 sequence that a site read with
// the wrong charset written back correctly. When the text is already correct —
// or when it cannot be proven to be double encoded — it is returned unchanged.
func RepairDoubleEncoded(text string) string {
	repaired, ok := repairDoubleEncoded(text)
	if !ok {
		return text
	}
	return repaired
}

// IsDoubleEncoded reports whether RepairDoubleEncoded would change text.
func IsDoubleEncoded(text string) bool {
	_, ok := repairDoubleEncoded(text)
	return ok
}

func repairDoubleEncoded(text string) (string, bool) {
	if text == "" {
		return text, false
	}
	raw := make([]byte, 0, len(text))
	high := false
	for _, r := range text {
		if r < utf8.RuneSelf {
			raw = append(raw, byte(r))
			continue
		}
		high = true
		// A C1 control is what Latin-1 leaves where the original byte had no
		// printable meaning; CP1252 gave it a printable character instead.
		switch {
		case r >= 0x80 && r <= 0x9F, r >= 0xA0 && r <= 0xFF:
			raw = append(raw, byte(r))
		default:
			b, mapped := cp1252Bytes[r]
			if !mapped {
				// A letter of a real alphabet: the text is not a byte soup.
				return text, false
			}
			raw = append(raw, b)
		}
	}
	if !high || !utf8.Valid(raw) {
		return text, false
	}
	repaired := string(raw)
	if repaired == text {
		return text, false
	}
	// What Latin-1 leaves behind is a title full of C1 controls, and a UTF-8
	// reading of it must not leave any: they would be visible in the UI and
	// in the magnet name.
	if strings.ContainsFunc(repaired, func(r rune) bool { return r >= 0x80 && r <= 0x9F }) {
		return text, false
	}
	return repaired, true
}
