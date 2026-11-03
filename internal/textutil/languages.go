package textutil

import "strings"

// languageNames maps the codes a user may type to the English name sources
// declare in a release's language list ("Italian,English"). The store keeps
// that list in English, so a filter typed as "ita" or "Italian" must end up
// with the same needle.
var languageNames = map[string]string{
	"it": "Italian", "ita": "Italian", "italian": "Italian", "italiano": "Italian",
	"en": "English", "eng": "English", "english": "English", "inglese": "English",
	"fr": "French", "fra": "French", "fre": "French", "french": "French", "francese": "French",
	"de": "German", "deu": "German", "ger": "German", "german": "German", "tedesco": "German",
	"es": "Spanish", "spa": "Spanish", "spanish": "Spanish", "spagnolo": "Spanish",
	"pt": "Portuguese", "por": "Portuguese", "portuguese": "Portuguese", "portoghese": "Portuguese",
	"ru": "Russian", "rus": "Russian", "russian": "Russian", "russo": "Russian",
	"ja": "Japanese", "jpn": "Japanese", "japanese": "Japanese", "giapponese": "Japanese",
	"zh": "Chinese", "chi": "Chinese", "zho": "Chinese", "chinese": "Chinese", "cinese": "Chinese",
	"ko": "Korean", "kor": "Korean", "korean": "Korean", "coreano": "Korean",
	"pl": "Polish", "pol": "Polish", "polish": "Polish", "polacco": "Polish",
	"nl": "Dutch", "nld": "Dutch", "dut": "Dutch", "dutch": "Dutch", "olandese": "Dutch",
	"sv": "Swedish", "swe": "Swedish", "swedish": "Swedish", "svedese": "Swedish",
	"no": "Norwegian", "nor": "Norwegian", "norwegian": "Norwegian", "norvegese": "Norwegian",
	"da": "Danish", "dan": "Danish", "danish": "Danish", "danese": "Danish",
	"fi": "Finnish", "fin": "Finnish", "finnish": "Finnish", "finlandese": "Finnish",
	"cs": "Czech", "ces": "Czech", "cze": "Czech", "czech": "Czech", "ceco": "Czech",
	"hu": "Hungarian", "hun": "Hungarian", "hungarian": "Hungarian", "ungherese": "Hungarian",
	"ro": "Romanian", "ron": "Romanian", "rum": "Romanian", "romanian": "Romanian", "rumeno": "Romanian",
	"el": "Greek", "ell": "Greek", "gre": "Greek", "greek": "Greek", "greco": "Greek",
	"tr": "Turkish", "tur": "Turkish", "turkish": "Turkish", "turco": "Turkish",
	"ar": "Arabic", "ara": "Arabic", "arabic": "Arabic", "arabo": "Arabic",
	"he": "Hebrew", "heb": "Hebrew", "hebrew": "Hebrew", "ebraico": "Hebrew",
	"hi": "Hindi", "hin": "Hindi", "hindi": "Hindi",
	"th": "Thai", "tha": "Thai", "thai": "Thai",
	"vi": "Vietnamese", "vie": "Vietnamese", "vietnamese": "Vietnamese",
	"uk": "Ukrainian", "ukr": "Ukrainian", "ukrainian": "Ukrainian", "ucraino": "Ukrainian",
	"bg": "Bulgarian", "bul": "Bulgarian", "bulgarian": "Bulgarian", "bulgaro": "Bulgarian",
	"hr": "Croatian", "hrv": "Croatian", "croatian": "Croatian", "croato": "Croatian",
	"sr": "Serbian", "srp": "Serbian", "serbian": "Serbian", "serbo": "Serbian",
	"sk": "Slovak", "slk": "Slovak", "slo": "Slovak", "slovak": "Slovak", "slovacco": "Slovak",
	"sl": "Slovenian", "slv": "Slovenian", "slovenian": "Slovenian", "sloveno": "Slovenian",
}

// LanguageNeedles turns what the user typed in the language filter into the
// strings a release must contain. It returns the English name when the input
// is a known code and the input itself otherwise, so any language a source
// declares can be asked for even when it is not in the table.
func LanguageNeedles(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	if len(raw) > 40 {
		raw = raw[:40]
	}
	if name, ok := languageNames[strings.ToLower(raw)]; ok {
		if strings.EqualFold(name, raw) {
			return []string{name}
		}
		return []string{name, raw}
	}
	return []string{raw}
}

// CommonLanguages lists the languages offered as suggestions in the filter.
// The filter is free text: this is a shortcut, not a closed list.
func CommonLanguages() []string {
	return []string{
		"Italian", "English", "French", "German", "Spanish", "Portuguese",
		"Russian", "Japanese", "Chinese", "Korean", "Polish", "Dutch",
		"Swedish", "Norwegian", "Danish", "Finnish", "Czech", "Hungarian",
		"Romanian", "Greek", "Turkish", "Arabic", "Hebrew", "Hindi",
		"Thai", "Vietnamese", "Ukrainian", "Bulgarian", "Croatian",
		"Serbian", "Slovak", "Slovenian",
	}
}
