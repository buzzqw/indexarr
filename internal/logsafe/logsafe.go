// Package logsafe wraps a slog handler and removes credentials from messages
// and string attributes before they reach the journal or the in-app log.
package logsafe

import (
	"context"
	"log/slog"
	"regexp"
	"strings"
)

var (
	querySecret = regexp.MustCompile(`(?i)([?&](?:apikey|api_key|passkey|rsskey|torrent_pass|token|password|secret|u|e)=)([^&\s"']+)`)
	bareSecret  = regexp.MustCompile(`(?i)\b(apikey|api_key|passkey|rsskey|torrent_pass|token|password|secret)=([^\s&"']+)`)
	headerAuth  = regexp.MustCompile(`(?i)((?:authorization|proxy-authorization|x-api-key|api-key)\s*:\s*(?:bearer\s+|basic\s+)?)([^\s"',;]{6,})`)
	bearerToken = regexp.MustCompile(`(?i)\b(bearer\s+)([A-Za-z0-9._~+/=-]{8,})`)
	webhookPath = regexp.MustCompile(`(?i)(/api/webhooks/\d+/)([^/\s?#"'&]+)`)
	telegramBot = regexp.MustCompile(`(/bot)(\d+:[A-Za-z0-9_-]{8,})`)
	jsonSecret  = regexp.MustCompile(`(?i)("(?:password|passwd|api_key|apikey|token|secret|cookie|authorization)"\s*:\s*")([^"]+)(")`)
	userInfo    = regexp.MustCompile(`://[^/\s:@]+:[^/\s@]+@`)
)

// Redact masks credential-looking query parameters and URL userinfo.
func Redact(text string) string {
	text = querySecret.ReplaceAllStringFunc(text, func(match string) string {
		parts := querySecret.FindStringSubmatch(match)
		if len(parts) != 3 {
			return match
		}
		return parts[1] + mask(parts[2])
	})
	text = bareSecret.ReplaceAllStringFunc(text, func(match string) string {
		parts := bareSecret.FindStringSubmatch(match)
		if len(parts) != 3 {
			return match
		}
		return parts[1] + "=" + mask(parts[2])
	})
	text = headerAuth.ReplaceAllStringFunc(text, func(match string) string {
		parts := headerAuth.FindStringSubmatch(match)
		if len(parts) != 3 {
			return match
		}
		return parts[1] + mask(parts[2])
	})
	text = bearerToken.ReplaceAllStringFunc(text, func(match string) string {
		parts := bearerToken.FindStringSubmatch(match)
		if len(parts) != 3 {
			return match
		}
		return parts[1] + mask(parts[2])
	})
	text = webhookPath.ReplaceAllStringFunc(text, func(match string) string {
		parts := webhookPath.FindStringSubmatch(match)
		if len(parts) != 3 {
			return match
		}
		return parts[1] + mask(parts[2])
	})
	text = telegramBot.ReplaceAllStringFunc(text, func(match string) string {
		parts := telegramBot.FindStringSubmatch(match)
		if len(parts) != 3 {
			return match
		}
		return parts[1] + mask(parts[2])
	})
	text = jsonSecret.ReplaceAllStringFunc(text, func(match string) string {
		parts := jsonSecret.FindStringSubmatch(match)
		if len(parts) != 4 {
			return match
		}
		return parts[1] + mask(parts[2]) + parts[3]
	})
	return userInfo.ReplaceAllString(text, "://***:***@")
}

func mask(value string) string {
	if len(value) <= 4 {
		return "…"
	}
	return value[:4] + "…"
}

type Handler struct{ next slog.Handler }

// NewHandler wraps next with credential redaction.
func NewHandler(next slog.Handler) slog.Handler { return &Handler{next: next} }

func (h *Handler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.next.Enabled(ctx, level)
}

func (h *Handler) Handle(ctx context.Context, record slog.Record) error {
	clone := slog.NewRecord(record.Time, record.Level, Redact(record.Message), record.PC)
	record.Attrs(func(attr slog.Attr) bool {
		clone.AddAttrs(redactAttr(attr))
		return true
	})
	return h.next.Handle(ctx, clone)
}

func (h *Handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	clean := make([]slog.Attr, len(attrs))
	for i, attr := range attrs {
		clean[i] = redactAttr(attr)
	}
	return &Handler{next: h.next.WithAttrs(clean)}
}

func (h *Handler) WithGroup(name string) slog.Handler {
	return &Handler{next: h.next.WithGroup(name)}
}

func redactAttr(attr slog.Attr) slog.Attr {
	switch attr.Value.Kind() {
	case slog.KindString:
		attr.Value = slog.StringValue(Redact(attr.Value.String()))
	case slog.KindGroup:
		group := attr.Value.Group()
		clean := make([]slog.Attr, len(group))
		for i, child := range group {
			clean[i] = redactAttr(child)
		}
		attr.Value = slog.GroupValue(clean...)
	case slog.KindAny:
		// logger.Error("...", "error", err) stores the error as KindAny.
		// Without this branch the message of an *url.Error or a wrapped
		// login failure reaches the log with the token still in it.
		if err, ok := attr.Value.Any().(error); ok && err != nil {
			attr.Value = slog.StringValue(Redact(err.Error()))
		}
	}
	return attr
}

// LooksSensitive is used by tests and callers that want an explicit check.
func LooksSensitive(text string) bool {
	lower := strings.ToLower(text)
	for _, needle := range []string{"apikey=", "passkey=", "rsskey=", "torrent_pass=", "password=", "secret="} {
		if strings.Contains(lower, needle) {
			return true
		}
	}
	return false
}
