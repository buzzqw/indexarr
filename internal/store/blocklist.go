package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Blocklist rule kinds.
const (
	BlockKindInfohash = "infohash"
	BlockKindTitle    = "title"
	BlockKindRegex    = "regex"
	BlockKindSource   = "source"
	BlockKindCategory = "category"
)

// BlocklistRule prevents matching releases from entering the cache again.
type BlocklistRule struct {
	ID        int64
	Kind      string
	Value     string
	Note      string
	CreatedAt time.Time
}

// ListBlocklist returns all rules, newest first.
func (s *Store) ListBlocklist(ctx context.Context) ([]BlocklistRule, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, kind, value, note, created_at FROM blocklist ORDER BY id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []BlocklistRule
	for rows.Next() {
		var rule BlocklistRule
		var created int64
		if err := rows.Scan(&rule.ID, &rule.Kind, &rule.Value, &rule.Note, &created); err != nil {
			return nil, err
		}
		rule.CreatedAt = unixTime(created)
		out = append(out, rule)
	}
	return out, rows.Err()
}

// AddBlocklistRule adds a rule, updating the note when the same kind/value
// already exists. It returns the rule id.
func (s *Store) AddBlocklistRule(ctx context.Context, kind, value, note string) (int64, error) {
	kind = strings.ToLower(strings.TrimSpace(kind))
	value = strings.TrimSpace(value)
	switch kind {
	case BlockKindInfohash, BlockKindTitle, BlockKindRegex, BlockKindSource, BlockKindCategory:
	default:
		return 0, fmt.Errorf("invalid blocklist kind %q", kind)
	}
	if value == "" {
		return 0, fmt.Errorf("blocklist value is required")
	}
	if kind == BlockKindRegex {
		if _, err := regexp.Compile("(?i)" + value); err != nil {
			return 0, fmt.Errorf("invalid regular expression %q: %v", value, err)
		}
	}
	if kind == BlockKindInfohash {
		value = strings.ToLower(value)
	}
	var id int64
	err := s.db.QueryRowContext(ctx, `
		INSERT INTO blocklist(kind, value, note, created_at) VALUES(?, ?, ?, ?)
		ON CONFLICT(kind, value) DO UPDATE SET note = excluded.note
		RETURNING id`, kind, value, strings.TrimSpace(note), time.Now().Unix()).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("add blocklist rule: %w", err)
	}
	return id, nil
}

// DeleteBlocklistRule removes one rule.
func (s *Store) DeleteBlocklistRule(ctx context.Context, id int64) (bool, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM blocklist WHERE id = ?`, id)
	if err != nil {
		return false, err
	}
	affected, _ := res.RowsAffected()
	return affected > 0, nil
}

// SetReleaseFlags updates the favorite/hidden flags of a release.
func (s *Store) SetReleaseFlags(ctx context.Context, id int64, favorite, hidden bool) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE releases SET favorite = ?, hidden = ? WHERE id = ?`,
		boolInt(favorite), boolInt(hidden), id)
	if err != nil {
		return false, err
	}
	affected, _ := res.RowsAffected()
	return affected > 0, nil
}

type compiledBlockRule struct {
	rule  BlocklistRule
	regex *regexp.Regexp
}

// BlocklistMatcher checks releases against the rules loaded at the start of a
// run. Regexes are compiled once.
type BlocklistMatcher struct {
	rules []compiledBlockRule
}

// NewBlocklistMatcher compiles the rules.
func NewBlocklistMatcher(rules []BlocklistRule) *BlocklistMatcher {
	matcher := &BlocklistMatcher{rules: make([]compiledBlockRule, 0, len(rules))}
	for _, rule := range rules {
		compiled := compiledBlockRule{rule: rule}
		if rule.Kind == BlockKindRegex {
			re, err := regexp.Compile("(?i)" + rule.Value)
			if err != nil {
				continue
			}
			compiled.regex = re
		}
		matcher.rules = append(matcher.rules, compiled)
	}
	return matcher
}

// Match returns the first matching rule, if any.
func (m *BlocklistMatcher) Match(infohash, title, sourceName, category string) (BlocklistRule, bool) {
	if m == nil {
		return BlocklistRule{}, false
	}
	lowerTitle := strings.ToLower(title)
	for _, compiled := range m.rules {
		rule := compiled.rule
		switch rule.Kind {
		case BlockKindInfohash:
			if infohash != "" && strings.EqualFold(infohash, rule.Value) {
				return rule, true
			}
		case BlockKindTitle:
			if strings.Contains(lowerTitle, strings.ToLower(rule.Value)) {
				return rule, true
			}
		case BlockKindRegex:
			if compiled.regex != nil && compiled.regex.MatchString(title) {
				return rule, true
			}
		case BlockKindSource:
			if sourceName != "" && strings.EqualFold(sourceName, rule.Value) {
				return rule, true
			}
		case BlockKindCategory:
			if blocklistCategoryMatch(category, rule.Value) {
				return rule, true
			}
		}
	}
	return BlocklistRule{}, false
}

func blocklistCategoryMatch(category, rule string) bool {
	category = strings.TrimSpace(category)
	rule = strings.TrimSpace(rule)
	if category == "" || rule == "" {
		return false
	}
	if len(rule) == 4 && strings.HasSuffix(rule, "000") {
		return strings.HasPrefix(category, rule[:1])
	}
	return category == rule
}

// ReleaseFlags returns the current flags of a release.
func (s *Store) ReleaseFlags(ctx context.Context, id int64) (favorite, hidden bool, err error) {
	var favoriteInt, hiddenInt int
	err = s.db.QueryRowContext(ctx,
		`SELECT favorite, hidden FROM releases WHERE id = ?`, id).Scan(&favoriteInt, &hiddenInt)
	if errors.Is(err, sql.ErrNoRows) {
		return false, false, err
	}
	return favoriteInt != 0, hiddenInt != 0, err
}
