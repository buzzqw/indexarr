package importers

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite"
)

// FromGextto reads the 34 source URLs from gextto's configuration database.
// The database is opened read-only: the importer never changes gextto. The
// returned targets are neutral and idempotent, so the action can be repeated.
func FromGextto(ctx context.Context, path string) ([]NativeTarget, []string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, nil, errors.New("importers: gextto database path is empty")
	}
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, nil, fmt.Errorf("importers: home directory: %w", err)
		}
		path = filepath.Join(home, strings.TrimPrefix(path, "~/"))
	}
	if _, err := os.Stat(path); err != nil {
		return nil, nil, fmt.Errorf("importers: gextto database: %w", err)
	}
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		return nil, nil, fmt.Errorf("importers: open gextto database: %w", err)
	}
	defer db.Close()
	var raw string
	if err := db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = 'url'`).Scan(&raw); err != nil {
		return nil, nil, fmt.Errorf("importers: read gextto sources: %w", err)
	}
	var urls []string
	if err := json.Unmarshal([]byte(raw), &urls); err != nil {
		return nil, nil, fmt.Errorf("importers: parse gextto sources: %w", err)
	}

	targets := make([]NativeTarget, 0, len(urls))
	warnings := make([]string, 0)
	for _, source := range urls {
		target, warning := gexttoTarget(source)
		if warning != "" {
			warnings = append(warnings, warning)
			continue
		}
		targets = append(targets, target)
	}
	return targets, warnings, nil
}

func gexttoTarget(source string) (NativeTarget, string) {
	parsed, err := url.Parse(strings.TrimSpace(source))
	if err != nil || parsed.Hostname() == "" {
		return NativeTarget{}, "una fonte gextto ha un URL non valido ed è stata saltata"
	}
	host := strings.ToLower(parsed.Hostname())
	query := parsed.Query()
	filters := query["filter"]
	switch {
	case host == "extto.org" || strings.HasSuffix(host, ".extto.org"):
		return exttoTarget(filters, query.Get("q")), ""
	case strings.Contains(host, "torrentgalaxy"):
		uploader := ""
		if strings.HasPrefix(parsed.Path, "/get-posts/user:") {
			uploader = strings.Trim(strings.TrimPrefix(parsed.Path, "/get-posts/user:"), "/")
		}
		if uploader == "" {
			return NativeTarget{}, "una fonte TorrentGalaxy non ha un uploader"
		}
		name := "TorrentGalaxy – " + uploader
		return NativeTarget{
			Name: name, Slug: slugify(name), DefinitionID: "torrentgalaxy",
			Config: map[string]string{"uploader": uploader}, Enabled: true,
			Protocol: ProtocolTorrent,
		}, ""
	case host == "rss.knaben.org" || strings.HasSuffix(host, ".knaben.org"):
		return NativeTarget{
			Name: "Knaben", Slug: "knaben", DefinitionID: "knaben",
			Config:  map[string]string{"url": source, "default-category": "8000"},
			Enabled: true, Protocol: ProtocolTorrent,
		}, ""
	case strings.Contains(host, "torrentleech"):
		// The path is the personal rsskey: it goes to the vault, never to
		// config_json, logs or the import summary.
		return NativeTarget{
			Name: "TorrentLeech (RSS)", Slug: "torrentleech-rss",
			DefinitionID: "torrentleech-rss",
			Secrets:      map[string]string{"url": source},
			Enabled:      true, Protocol: ProtocolTorrent,
		}, ""
	default:
		return NativeTarget{}, "una fonte gextto non è riconosciuta ed è stata saltata"
	}
}

func exttoTarget(filters []string, fixedQuery string) NativeTarget {
	filters = append([]string(nil), filters...)
	q := strings.TrimSpace(fixedQuery)
	name := "ExtTo"
	config := map[string]string{}
	if q != "" {
		name = "ExtTo – " + q
		// The definition requires a non-empty filter, but sending an empty
		// filter= makes ExtTo return zero rows. c=TV is accepted with a fixed
		// query and does not discard the q results.
		config["filter"] = "c=TV"
	} else {
		if len(filters) > 0 {
			config["filter"] = filters[0]
			if uploader := strings.TrimPrefix(filters[0], "u="); uploader != filters[0] && uploader != "" {
				name = "ExtTo – " + uploader
			}
		} else {
			config["filter"] = "c=TV"
		}
		if len(filters) > 1 {
			config["filter-extra"] = filters[1]
		}
	}
	if q == "" && len(filters) > 1 && strings.HasPrefix(filters[0], "c=") {
		category := strings.TrimPrefix(filters[0], "c=")
		tag := strings.TrimPrefix(filters[1], "tg=")
		if tag != filters[1] {
			name = "ExtTo – " + tag + " " + category
		}
	}
	if q != "" {
		config["query"] = q
	}
	return NativeTarget{
		Name: name, Slug: slugify(name), DefinitionID: "extto",
		Config: config, Enabled: true, Protocol: ProtocolTorrent,
	}
}
