package definition

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"sync"

	"gopkg.in/yaml.v3"
)

// CurrentSchema is the definition format version understood by this package.
const CurrentSchema = 1

// Upgrader transforms a definition from one schema version to the next.
type Upgrader func(map[string]any) (map[string]any, error)

var (
	// ErrFutureSchema is returned when a definition declares a schema newer
	// than CurrentSchema.
	ErrFutureSchema = errors.New("definition: schema version is newer than supported")
	// ErrNoUpgrader is returned when the upgrade chain is incomplete.
	ErrNoUpgrader = errors.New("definition: no upgrader registered")
)

var (
	upgradersMu sync.RWMutex
	upgraders   = make(map[int]Upgrader)
)

// RegisterUpgrader installs the transformation from fromVersion to
// fromVersion+1. The chain must be complete up to CurrentSchema.
func RegisterUpgrader(fromVersion int, fn Upgrader) error {
	if fn == nil {
		return errors.New("definition: upgrader must not be nil")
	}
	if fromVersion < 0 {
		return fmt.Errorf("definition: invalid upgrader source version %d", fromVersion)
	}
	if fromVersion >= CurrentSchema {
		return fmt.Errorf("definition: upgrader from version %d is not needed (current schema %d)", fromVersion, CurrentSchema)
	}

	upgradersMu.Lock()
	defer upgradersMu.Unlock()
	if _, exists := upgraders[fromVersion]; exists {
		return fmt.Errorf("definition: upgrader from version %d already registered", fromVersion)
	}
	upgraders[fromVersion] = fn
	return nil
}

// Upgrade brings an arbitrary definition document to CurrentSchema.
//
// It decodes YAML into a generic map so that unknown fields survive the
// upgrade, applies the registered chain of upgraders, then re-marshals the
// result deterministically (map keys are sorted by yaml.v3). A missing schema
// key is treated as version 1.
func Upgrade(body []byte) (canonical []byte, version int, err error) {
	var raw map[string]any
	if err := yaml.Unmarshal(body, &raw); err != nil {
		return nil, 0, fmt.Errorf("definition: YAML: %w", err)
	}
	if raw == nil {
		raw = make(map[string]any)
	}

	current := CurrentSchema
	if value, ok := raw["schema"]; ok && value != nil {
		current, err = schemaVersion(value)
		if err != nil {
			return nil, 0, err
		}
	}
	if current > CurrentSchema {
		return nil, current, fmt.Errorf("%w: %d (current %d)", ErrFutureSchema, current, CurrentSchema)
	}

	for from := current; from < CurrentSchema; from++ {
		upgradersMu.RLock()
		fn, ok := upgraders[from]
		upgradersMu.RUnlock()
		if !ok {
			return nil, current, fmt.Errorf("%w: v%d -> v%d", ErrNoUpgrader, from, from+1)
		}

		next, upgradeErr := fn(raw)
		if upgradeErr != nil {
			return nil, current, fmt.Errorf("definition: upgrade v%d -> v%d: %w", from, from+1, upgradeErr)
		}
		if next == nil {
			return nil, current, fmt.Errorf("definition: upgrade v%d -> v%d returned nil", from, from+1)
		}
		raw = next
	}

	raw["schema"] = CurrentSchema
	canonical, err = yaml.Marshal(normalizeValue(raw))
	if err != nil {
		return nil, current, fmt.Errorf("definition: marshal upgraded YAML: %w", err)
	}
	return canonical, CurrentSchema, nil
}

func schemaVersion(value any) (int, error) {
	switch v := value.(type) {
	case int:
		return v, nil
	case int8:
		return int(v), nil
	case int16:
		return int(v), nil
	case int32:
		return int(v), nil
	case int64:
		if v < math.MinInt || v > math.MaxInt {
			return 0, fmt.Errorf("definition: schema version %d is out of range", v)
		}
		return int(v), nil
	case uint:
		if uint64(v) > math.MaxInt {
			return 0, fmt.Errorf("definition: schema version %d is out of range", v)
		}
		return int(v), nil
	case uint8:
		return int(v), nil
	case uint16:
		return int(v), nil
	case uint32:
		return int(v), nil
	case uint64:
		if v > math.MaxInt {
			return 0, fmt.Errorf("definition: schema version %d is out of range", v)
		}
		return int(v), nil
	case float32:
		return schemaFloat(float64(v))
	case float64:
		return schemaFloat(v)
	case string:
		parsed, err := strconv.Atoi(v)
		if err != nil {
			return 0, fmt.Errorf("definition: invalid schema version %q", v)
		}
		return parsed, nil
	default:
		return 0, fmt.Errorf("definition: invalid schema version %v", value)
	}
}

func schemaFloat(v float64) (int, error) {
	if math.IsNaN(v) || math.IsInf(v, 0) || v != math.Trunc(v) {
		return 0, fmt.Errorf("definition: invalid schema version %v", v)
	}
	if v < math.MinInt || v > math.MaxInt {
		return 0, fmt.Errorf("definition: schema version %v is out of range", v)
	}
	return int(v), nil
}

// normalizeValue converts the map types produced by YAML (and any other
// decoder) into plain strings-keyed maps so marshaling is deterministic.
func normalizeValue(value any) any {
	switch v := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(v))
		for key, item := range v {
			out[key] = normalizeValue(item)
		}
		return out
	case map[any]any:
		out := make(map[string]any, len(v))
		for key, item := range v {
			out[fmt.Sprint(key)] = normalizeValue(item)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = normalizeValue(item)
		}
		return out
	default:
		return value
	}
}
