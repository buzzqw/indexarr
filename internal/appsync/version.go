package appsync

import (
	"encoding/json"
	"strconv"
	"strings"
)

// versionFromBody extracts a version from the common response shapes:
//
//	{"version":"1.2.3"}
//	{"data":{"version":"1.2.3"}}
//	{"result":"1.2.3"}
//
// A short plain-text body is accepted as well, for builds that do not wrap
// the response in JSON.
func versionFromBody(body []byte) string {
	trimmed := strings.TrimSpace(string(body))
	if trimmed == "" {
		return ""
	}
	var payload any
	if err := json.Unmarshal(body, &payload); err != nil {
		if len(trimmed) > 200 || strings.HasPrefix(trimmed, "<") {
			return ""
		}
		return trimmed
	}
	return findVersion(payload, 0)
}

func findVersion(value any, depth int) string {
	if depth > 4 {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case json.Number:
		return typed.String()
	case map[string]any:
		for _, key := range []string{
			"version", "current_version", "currentVersion", "appVersion", "build",
		} {
			if version := findVersion(typed[key], depth+1); version != "" {
				return version
			}
		}
		for _, key := range []string{"data", "result", "response"} {
			if version := findVersion(typed[key], depth+1); version != "" {
				return version
			}
		}
	}
	return ""
}
