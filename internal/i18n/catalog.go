// Package i18n holds the embedded UI catalogs and the language negotiation
// used by the web interface. English is the default and the fallback for every
// missing key; Italian ships as a complete translation.
package i18n

import (
	"embed"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Language is a BCP-47 language subtag supported by the interface.
type Language string

const (
	EN Language = "en"
	IT Language = "it"
	ES Language = "es"
	FR Language = "fr"
	DE Language = "de"
	ZH Language = "zh"
	PL Language = "pl"
)

//go:embed locales/*.json
var localeFiles embed.FS

var supported = []Language{EN, IT, ES, FR, DE, ZH, PL}

var languageNames = map[Language]string{
	EN: "English",
	IT: "Italiano",
	ES: "Español",
	FR: "Français",
	DE: "Deutsch",
	ZH: "中文",
	PL: "Polski",
}

var catalogs = map[Language]map[string]string{}

var english map[string]string

func init() {
	for _, lang := range supported {
		data, err := localeFiles.ReadFile("locales/" + string(lang) + ".json")
		if err != nil {
			panic(fmt.Sprintf("i18n: read %s catalog: %v", lang, err))
		}
		entries := map[string]string{}
		if err := json.Unmarshal(data, &entries); err != nil {
			panic(fmt.Sprintf("i18n: parse %s catalog: %v", lang, err))
		}
		catalogs[lang] = entries
	}
	english = catalogs[EN]
}

// Supported returns the languages in the order shown by the selector.
func Supported() []Language {
	out := make([]Language, len(supported))
	copy(out, supported)
	return out
}

// Lookup normalizes a raw tag and reports whether it is supported. It accepts
// forms such as "it", "it-IT" and "it_IT".
func Lookup(raw string) (Language, bool) {
	tag := strings.TrimSpace(raw)
	tag = strings.ReplaceAll(tag, "_", "-")
	if index := strings.IndexByte(tag, '-'); index >= 0 {
		tag = tag[:index]
	}
	tag = strings.ToLower(tag)
	for _, lang := range supported {
		if string(lang) == tag {
			return lang, true
		}
	}
	return EN, false
}

// Normalize maps a raw tag to a supported language, falling back to English.
func Normalize(raw string) Language {
	lang, _ := Lookup(raw)
	return lang
}

// Detect resolves the request language. A query parameter wins over the saved
// cookie, which wins over Accept-Language; English is the final fallback.
func Detect(acceptLanguage, cookie, query string) Language {
	if strings.TrimSpace(query) != "" {
		if lang, ok := Lookup(query); ok {
			return lang
		}
		return EN
	}
	if strings.TrimSpace(cookie) != "" {
		if lang, ok := Lookup(cookie); ok {
			return lang
		}
	}
	if lang, ok := fromAcceptLanguage(acceptLanguage); ok {
		return lang
	}
	return EN
}

func fromAcceptLanguage(header string) (Language, bool) {
	type candidate struct {
		lang  Language
		q     float64
		order int
	}
	var candidates []candidate
	for order, part := range strings.Split(header, ",") {
		fields := strings.Split(part, ";")
		lang, ok := Lookup(fields[0])
		if !ok {
			continue
		}
		quality := 1.0
		for _, parameter := range fields[1:] {
			name, value, found := strings.Cut(strings.TrimSpace(parameter), "=")
			if !found || !strings.EqualFold(strings.TrimSpace(name), "q") {
				continue
			}
			parsed, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
			if err == nil {
				quality = parsed
			}
		}
		if quality <= 0 {
			continue
		}
		candidates = append(candidates, candidate{lang: lang, q: quality, order: order})
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].q != candidates[j].q {
			return candidates[i].q > candidates[j].q
		}
		return candidates[i].order < candidates[j].order
	})
	if len(candidates) == 0 {
		return EN, false
	}
	return candidates[0].lang, true
}

// Name is the endonym shown in the language selector.
func (l Language) Name() string {
	if name, ok := languageNames[l]; ok {
		return name
	}
	return languageNames[EN]
}

// HTML is the value used by the document lang attribute.
func (l Language) HTML() string {
	if _, ok := catalogs[l]; ok {
		return string(l)
	}
	return string(EN)
}

// T returns the translation of key in lang. Missing keys fall back to English,
// and an unknown key is returned unchanged. When args are supplied the message
// is formatted with fmt.Sprintf, so catalogs can use %s and %d verbs.
func T(lang Language, key string, args ...any) string {
	message, ok := catalogs[lang][key]
	if !ok || message == "" {
		message, ok = english[key]
	}
	if !ok || message == "" {
		message, ok = defaultEnglish[key]
	}
	if !ok {
		return key
	}
	if len(args) == 0 {
		return message
	}
	return fmt.Sprintf(message, args...)
}

// Keys returns the English keys sorted, used by the catalog parity tests.
func Keys() []string {
	keys := make([]string, 0, len(english)+len(defaultEnglish))
	seen := make(map[string]struct{}, len(english)+len(defaultEnglish))
	for key := range english {
		keys = append(keys, key)
		seen[key] = struct{}{}
	}
	for key := range defaultEnglish {
		if _, ok := seen[key]; !ok {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	return keys
}
