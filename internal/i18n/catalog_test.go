package i18n

import "testing"

func TestNormalize(t *testing.T) {
	cases := map[string]Language{
		"en":         EN,
		"en-US":      EN,
		"EN_us":      EN,
		" it-IT ":    IT,
		"it":         IT,
		"fr-CA":      FR,
		"de-DE":      DE,
		"es-419":     ES,
		"":           EN,
		"xx-YY":      EN,
		"zh-CN":      ZH,
		"pl-PL":      PL,
		"portuguese": EN,
	}
	for raw, want := range cases {
		if got := Normalize(raw); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestDetectPrecedence(t *testing.T) {
	cases := []struct {
		name           string
		acceptLanguage string
		cookie         string
		query          string
		want           Language
	}{
		{"default is English", "", "", "", EN},
		{"query wins over cookie", "fr-FR", "it", "de", DE},
		{"query wins over Accept-Language", "it-IT", "", "en", EN},
		{"cookie wins over Accept-Language", "fr-FR", "it", "", IT},
		{"Accept-Language is used last", "fr-FR,fr;q=0.9,en;q=0.5", "", "", FR},
		{"Accept-Language quality order", "en;q=0.4, de-DE;q=0.9, it;q=0.7", "", "", DE},
		{"unsupported Accept-Language falls back", "pt-BR, pt;q=0.9", "", "", EN},
		{"unsupported query falls back to English", "", "it", "xx", EN},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Detect(tc.acceptLanguage, tc.cookie, tc.query); got != tc.want {
				t.Errorf("Detect(%q, %q, %q) = %q, want %q",
					tc.acceptLanguage, tc.cookie, tc.query, got, tc.want)
			}
		})
	}
}

func TestFallbackAndFormatting(t *testing.T) {
	if got := T(IT, "shell.overview"); got != "Panoramica" {
		t.Errorf("Italian shell.overview = %q", got)
	}
	if got, want := T(FR, "indexer.testResult"), "Résultat du test"; got != want {
		t.Errorf("French translation = %q, want %q", got, want)
	}
	if got, want := T(EN, "missing.key"), "missing.key"; got != want {
		t.Errorf("missing key = %q, want %q", got, want)
	}
	if got, want := T(EN, "dashboard.itemsValue", 2, 5), "2 new / 5 seen"; got != want {
		t.Errorf("English formatting = %q, want %q", got, want)
	}
	if got, want := T(IT, "dashboard.itemsValue", 2, 5), "2 nuovi / 5 visti"; got != want {
		t.Errorf("Italian formatting = %q, want %q", got, want)
	}
	if got := T(DE, "shell.searchPlaceholder"); got != "Releases durchsuchen…" {
		t.Errorf("German placeholder = %q", got)
	}
}

func TestCatalogParity(t *testing.T) {
	if len(english) == 0 {
		t.Fatal("the English catalog is empty")
	}
	for key, value := range english {
		if value == "" {
			t.Errorf("English key %q has an empty value", key)
		}
	}
	for _, lang := range Supported() {
		if lang == EN {
			continue
		}
		for key, value := range catalogs[lang] {
			if _, ok := english[key]; !ok {
				t.Errorf("%s catalog key %q is missing from English", lang, key)
			}
			if value == "" {
				t.Errorf("%s key %q has an empty value", lang, key)
			}
		}
	}
	// Every shipped catalog is complete; missing keys fall back to English at
	// runtime, but the packaged files must stay in sync.
	for _, lang := range Supported() {
		if lang == EN {
			continue
		}
		for key := range english {
			if _, ok := catalogs[lang][key]; !ok {
				t.Errorf("%s catalog is missing English key %q", lang, key)
			}
		}
	}
}

func TestNamesAndHTML(t *testing.T) {
	wantNames := map[Language]string{
		EN: "English", IT: "Italiano", ES: "Español", FR: "Français", DE: "Deutsch",
		ZH: "中文", PL: "Polski",
	}
	for lang, want := range wantNames {
		if got := lang.Name(); got != want {
			t.Errorf("%s.Name() = %q, want %q", lang, got, want)
		}
		if got := lang.HTML(); got != string(lang) {
			t.Errorf("%s.HTML() = %q", lang, got)
		}
	}
	if got := Language("xx").Name(); got != "English" {
		t.Errorf("unknown language Name() = %q", got)
	}
	if got := Language("xx").HTML(); got != "en" {
		t.Errorf("unknown language HTML() = %q", got)
	}
}

func TestSupportedIsACopy(t *testing.T) {
	langs := Supported()
	if len(langs) == 0 || langs[0] != EN {
		t.Fatalf("Supported() = %v", langs)
	}
	langs[0] = IT
	if Supported()[0] != EN {
		t.Error("Supported() exposed the internal slice")
	}
}
