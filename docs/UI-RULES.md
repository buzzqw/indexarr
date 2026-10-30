# Interface rules

These rules apply to every page of the indexarr web interface and to every
template change. `scripts/ui-smoke.sh` verifies the routes and the redirects.

## 1. Rules

1. **One concept: the indexer, that is a site.** The words *definition*,
   *family*, *connector*, *slug*, *SSRF* do not appear outside "Advanced"
   and the tools for definition authors.
2. **One main action per page**, top right, filled button. The other actions
   are secondary (outline) or in a "⋯" menu.
3. **At most 5 visible fields in a form.** The rest goes into an "Advanced"
   `<details>` block, closed, with sensible values already in place.
4. **Never ask what can be deduced.** Protocol, name in the URL, categories,
   URL of a known site, user agent: they are derived from the definition.
5. **Every empty page says what to do** with a button, not with a paragraph.
6. **Every error has a cause and a remedy in one sentence**, with the action one
   click away ("The site responds 429: too many requests. [Reduce the
   frequency]"). The remedy changes only the source that has the problem.
7. **One number, one source.** If two parts of the page talk about the same
   state they read the same value from the model. No "169 errors" and "all
   good" on the same screen.
8. **Missing data is written as "—"**, never `0`, `?` or an ambiguous empty
   value.
9. **Secrets are not shown until the user asks for them** ("Show" or "Copy"
   button). Never in links, in titles, in example URLs.
10. **Every flow ends with the next step**: confirmation message and a button
    toward what is likely needed next.
11. **Problem ≠ characteristic.** Red, triangles and "Problems" only for
    errors, expired login, unreachable site or zero results when the site
    should have some. A missing date, size, seeder, magnet or infohash are
    characteristics of the site: they go in a neutral section "What this site
    provides", never in the Home or in the badge. A slow source that ends up in
    the background is not an error.
12. **Categories by name.** The user never sees "Category N" or a bare ID:
    standard categories are shown grouped by family with the name, and the
    number only under "Advanced".
13. **One action bar.** In cards and forms the buttons sit on the same baseline;
    the primary action is on the right, the secondary ones on the left; below
    600 px they stack full width. In tables the actions have an aligned column.
    Buttons, button-links and selects use a single height token.
14. **One explanatory paragraph at the bottom of every page.** Every page with a
    function has, under the content, the `page-help` component: translated into
    all 7 languages, collapsible (`<details>`), open by default only the first
    time and remembered per browser (`localStorage`, key
    `indexarr.help.<pagina>`). It says in simple words **what the page is for**,
    **how to use it** and **why**. The keys are `pageHelp.<pagina>`; a test fails
    if a page or a language does not have them. Every field, badge, state and
    non-obvious button has a `title` taken from the catalog, not literal text.
15. **Nothing is removed.** A rewritten page contains at least everything that
    was there: functions, buttons, forms, fields, links, columns, tooltips and
    help texts. Every action handled by a handler is reachable from the UI.

Text rules:

- Buttons with a verb: "Add indexer", "Try", "Save", "Connect Sonarr". Never
  "OK", "Send", "Submit".
- Short sentences, second person, no jargon. Help text goes under the field,
  one line.
- All strings go through `T` (i18n) and are really translated into it, en,
  de, fr, es, pl, zh. No literal text in the templates.
- Logs and notifications speak in sentences: what happened, what is wrong, what
  to do. Technical text stays in the details.

## 2. Menu structure

| Item | Content |
|---|---|
| Home | Status, endpoint to copy, next step |
| Search | Search in the releases |
| Indexers | Single list of the sites, detail, guided addition from the catalog |
| App | \*arr apps and download clients |
| Statistics | App requests, overview, downloads, runs, new releases, network |
| Logs | System log only |
| Settings | Sections, rules, maintenance, definitions |

Old URLs respond with a redirect to the new location. The badge next to an item
appears only when there is something to do.

## 3. Adding an indexer

The natural gesture is "I look for the site": first the search in the catalog,
then a small form.

- The form shows **only** the definition's `settings` with `required: true`,
  plus Name (pre-filled), Categories (checkboxes with the names, pre-filled from
  the definition) and Frequency (0 or empty uses the default; minimum 60
  seconds).
- It never shows the protocol, the URL of a site with `meta.urls` (it is chosen
  from a menu in Advanced only if there is more than one) or numeric category
  IDs.
- A pasted address opens the verified definition that recognizes it, or the
  Torznab/Newznab form (`/api`, `t=caps`) or the Feed form (RSS/Atom).
- **Add and test** saves, immediately runs the test in stages (reachable,
  search, download) and takes you to the detail with the result visible.
- The field order is the one in this list, not the one in the definition file.

## 4. Reused components

| Component | Use | Notes |
|---|---|---|
| `status-dot` | state of indexer, app, client | 5 states: ok, degraded, problem, disabled, trying; text for screen readers |
| `problem` | problem row with cause and action | `Problem{Text, ActionLabel, ActionURL}` |
| `secret-field` | API key, password | masked, "Show", "Copy"; the value is not in the HTML until "Show" is pressed |
| `copy-field` | endpoint URLs | Copy button with "Copied" confirmation |
| `advanced` | `<details>` "Advanced" | closed by default; open if it contains a validation error |
| `empty` | empty state | title, one sentence, one button |
| `next-step` | message after an action | flash with button |
| `category-picker` | categories | checkboxes with localized names, never IDs; Adult not checked by default |
| `action-bar` | form and card actions | primary on the right, secondary on the left, same height; column below 600 px |
| `site-picker` | site search | server-side rendering and a small JS filter; it works without JS too |
| `confirm` | destructive actions | says what is lost ("Delete ExtTo and 312 releases?") |
| `log_row` | one log row | sentence, area, "What is wrong", "What to do", collapsible technical details |
| `.skip` | "Skip to content" link | visible only on focus |

No JS framework: server-side rendering, `fetch` for fragments where needed,
progressive enhancement. The pages stay usable without JS.

## 5. How it is verified

- `make ci` runs the template tests (help paragraph, translations, tooltips,
  maintenance form), the route smoke test and the browser test
  (`scripts/ui-e2e.py`), including the 390 px width without horizontal
  scrolling and touch targets of at least 44 px.
- For every template touched, the `action=`, `name=` and `value=` values, the
  forms, the `<p>` paragraphs, the `title`s and the links are compared before
  and after: no count may go down.
