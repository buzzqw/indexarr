package web

import (
	"strings"
	"testing"

	"github.com/buzzqw/indexarr/internal/i18n"
)

// The donation link is part of the shell: it must stay on every page, open in
// a new tab and carry the translated label and tooltip.
func TestDonateLinkIsPresentAndTranslated(t *testing.T) {
	env := newTestEnv(t)
	const href = `https://www.paypal.com/cgi-bin/webscr?cmd=_donations&amp;business=azanzani@gmail.com&amp;item_name=Support+indexarr+Project`
	for _, lang := range i18n.Supported() {
		body := get(t, env.handler, "/?lang="+string(lang)).Body.String()
		for _, want := range []string{
			`class="donate"`,
			`href="` + href + `"`,
			`target="_blank"`,
			`rel="noopener noreferrer"`,
			`title="` + i18n.T(lang, "shell.donateTitle") + `"`,
			`aria-label="` + i18n.T(lang, "shell.donateTitle") + `"`,
			`>` + i18n.T(lang, "shell.donate") + `<`,
		} {
			if !strings.Contains(body, want) {
				t.Errorf("%s: donate link missing %q", lang, want)
			}
		}
	}
}
