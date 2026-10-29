package notify

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/mail"
	"net/smtp"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// delivery is one configured channel and its delivery function.
type delivery struct {
	name string
	run  func(context.Context) error
}

// outbound builds the channel list from the config. Every configured channel
// uses the same message and title, so all the services say the same thing.
func outbound(cfg Config, event Event, message, title string) []delivery {
	var jobs []delivery
	if endpoint := strings.TrimSpace(cfg.WebhookURL); endpoint != "" {
		jobs = append(jobs, delivery{"webhook", func(ctx context.Context) error {
			return sendWebhook(ctx, cfg, endpoint, event, title, message)
		}})
	}
	if token, chat := strings.TrimSpace(cfg.TelegramToken), strings.TrimSpace(cfg.TelegramChatID); token != "" && chat != "" {
		jobs = append(jobs, delivery{"telegram", func(ctx context.Context) error {
			return sendTelegram(ctx, cfg, token, chat, message)
		}})
	}
	if endpoint := strings.TrimSpace(cfg.AppriseURL); endpoint != "" {
		jobs = append(jobs, delivery{"apprise", func(ctx context.Context) error {
			return sendApprise(ctx, cfg, endpoint, event, message, title)
		}})
	}
	if endpoint := strings.TrimSpace(cfg.DiscordURL); endpoint != "" {
		jobs = append(jobs, delivery{"discord", func(ctx context.Context) error {
			return sendDiscord(ctx, cfg, endpoint, message)
		}})
	}
	if endpoint := strings.TrimSpace(cfg.SlackURL); endpoint != "" {
		jobs = append(jobs, delivery{"slack", func(ctx context.Context) error {
			return sendSlack(ctx, cfg, endpoint, message)
		}})
	}
	if endpoint, token := strings.TrimSpace(cfg.GotifyURL), strings.TrimSpace(cfg.GotifyToken); endpoint != "" && token != "" {
		jobs = append(jobs, delivery{"gotify", func(ctx context.Context) error {
			return sendGotify(ctx, cfg, endpoint, token, event, message, title)
		}})
	}
	if endpoint, topic := strings.TrimSpace(cfg.NtfyURL), strings.TrimSpace(cfg.NtfyTopic); endpoint != "" && topic != "" {
		jobs = append(jobs, delivery{"ntfy", func(ctx context.Context) error {
			return sendNtfy(ctx, cfg, endpoint, topic, event, message, title)
		}})
	}
	if token, user := strings.TrimSpace(cfg.PushoverToken), strings.TrimSpace(cfg.PushoverUser); token != "" && user != "" {
		jobs = append(jobs, delivery{"pushover", func(ctx context.Context) error {
			return sendPushover(ctx, cfg, token, user, event, message, title)
		}})
	}
	if strings.TrimSpace(cfg.SMTPHost) != "" && strings.TrimSpace(cfg.SMTPFrom) != "" && strings.TrimSpace(cfg.SMTPTo) != "" {
		jobs = append(jobs, delivery{"email", func(ctx context.Context) error {
			return sendEmail(ctx, cfg, message, title)
		}})
	}
	if path := strings.TrimSpace(cfg.ScriptPath); path != "" {
		jobs = append(jobs, delivery{"script", func(ctx context.Context) error {
			return sendScript(ctx, cfg, event, message)
		}})
	}
	return jobs
}

// webhookPayload is the JSON body of the webhook channel: the event fields
// plus the rendered subject and message, so a script can reuse the same words
// every other channel shows without rebuilding them.
type webhookPayload struct {
	Event
	Subject string `json:"subject"`
	Message string `json:"message"`
}

func sendWebhook(ctx context.Context, cfg Config, endpoint string, event Event, title, message string) error {
	body, err := json.Marshal(webhookPayload{Event: event, Subject: title, Message: message})
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return scrubSecrets(err, cfg.secrets()...)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", "indexarr")
	return doRequest(request, cfg.secrets()...)
}

func sendTelegram(ctx context.Context, cfg Config, token, chatID, message string) error {
	values := url.Values{}
	values.Set("chat_id", chatID)
	values.Set("text", message)
	values.Set("disable_web_page_preview", "true")
	endpoint := strings.TrimRight(TelegramAPIBase, "/") + "/bot" + token + "/sendMessage"
	return postForm(ctx, endpoint, values, nil, cfg.secrets()...)
}

func sendApprise(ctx context.Context, cfg Config, base string, event Event, message, title string) error {
	endpoint := strings.TrimRight(base, "/")
	if !strings.HasSuffix(strings.ToLower(endpoint), "/notify") {
		endpoint += "/notify"
	}
	if key := strings.TrimSpace(cfg.AppriseKey); key != "" {
		endpoint += "/" + url.PathEscape(key)
	}
	payload := map[string]string{
		"title": title,
		"body":  message,
		"type":  appriseType(event),
	}
	return postJSON(ctx, endpoint, payload, nil, cfg.secrets()...)
}

func appriseType(event Event) string {
	switch event.Kind {
	case KindGrabFailed, KindBackupFailed, KindRunFailed:
		return "failure"
	case KindIndexerState:
		switch strings.ToLower(strings.TrimSpace(event.Current)) {
		case StateError, StateBlocked:
			return "failure"
		case StateDegraded, StateDisabled:
			return "warning"
		case StateOK:
			return "success"
		}
	case KindGrabSuccess, KindTest:
		return "success"
	}
	return "info"
}

func sendDiscord(ctx context.Context, cfg Config, endpoint, message string) error {
	payload := map[string]string{
		"content":  truncateRunes(message, 1900),
		"username": "indexarr",
	}
	return postJSON(ctx, endpoint, payload, nil, cfg.secrets()...)
}

func sendSlack(ctx context.Context, cfg Config, endpoint, message string) error {
	payload := map[string]string{"text": truncateRunes(message, 3900)}
	return postJSON(ctx, endpoint, payload, nil, cfg.secrets()...)
}

func sendGotify(ctx context.Context, cfg Config, endpoint, token string, event Event, message, title string) error {
	payload := map[string]any{
		"title":    title,
		"message":  message,
		"priority": priorityFor(event),
	}
	headers := map[string]string{"X-Gotify-Key": token}
	return postJSON(ctx, strings.TrimRight(endpoint, "/")+"/message", payload, headers, cfg.secrets()...)
}

func sendNtfy(ctx context.Context, cfg Config, endpoint, topic string, event Event, message, title string) error {
	endpoint = strings.TrimRight(endpoint, "/") + "/" + url.PathEscape(strings.Trim(topic, "/"))
	request, err := http.NewRequestWithContext(ctx, http.MethodPost,
		endpoint, strings.NewReader(message))
	if err != nil {
		return scrubSecrets(err, cfg.secrets()...)
	}
	request.Header.Set("Content-Type", "text/plain; charset=utf-8")
	request.Header.Set("Title", title)
	request.Header.Set("Priority", strconv.Itoa(priorityFor(event)))
	request.Header.Set("Tags", "indexarr")
	if token := strings.TrimSpace(cfg.NtfyToken); token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	return doRequest(request, cfg.secrets()...)
}

func sendPushover(ctx context.Context, cfg Config, token, user string, event Event, message, title string) error {
	values := url.Values{}
	values.Set("token", token)
	values.Set("user", user)
	values.Set("message", truncateRunes(message, 1024))
	values.Set("title", truncateRunes(title, 250))
	values.Set("priority", strconv.Itoa(pushoverPriority(event)))
	return postForm(ctx, PushoverAPIBase, values, nil, cfg.secrets()...)
}

func priorityFor(event Event) int {
	switch event.Kind {
	case KindGrabFailed, KindBackupFailed, KindRunFailed:
		return 8
	case KindIndexerState:
		switch strings.ToLower(strings.TrimSpace(event.Current)) {
		case StateError, StateBlocked:
			return 8
		case StateDegraded, StateDisabled:
			return 5
		}
	}
	return 3
}

func pushoverPriority(event Event) int {
	switch event.Kind {
	case KindGrabFailed, KindBackupFailed, KindRunFailed:
		return 1
	case KindIndexerState:
		switch strings.ToLower(strings.TrimSpace(event.Current)) {
		case StateError, StateBlocked:
			return 1
		}
	}
	return 0
}

// sendEmail delivers the message over SMTP. STARTTLS is explicit: port 587
// usually advertises it, and a server that does not is reported instead of
// silently sending credentials in clear.
func sendEmail(ctx context.Context, cfg Config, message, title string) error {
	addr := strings.TrimSpace(cfg.SMTPHost)
	host, _, err := net.SplitHostPort(addr)
	if err != nil || strings.TrimSpace(host) == "" {
		return fmt.Errorf("SMTP server must be host:port")
	}
	from, err := mail.ParseAddress(strings.TrimSpace(cfg.SMTPFrom))
	if err != nil {
		return fmt.Errorf("invalid sender address: %w", err)
	}
	recipients, err := parseRecipients(cfg.SMTPTo)
	if err != nil {
		return err
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(DefaultTimeout)
	}
	dialer := net.Dialer{Timeout: time.Until(deadline)}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return scrubSecrets(err, cfg.secrets()...)
	}
	defer conn.Close()
	_ = conn.SetDeadline(deadline)
	client, err := smtp.NewClient(conn, host)
	if err != nil {
		return scrubSecrets(err, cfg.secrets()...)
	}
	defer client.Close()
	if cfg.SMTPStartTLS {
		if supported, _ := client.Extension("STARTTLS"); !supported {
			return fmt.Errorf("SMTP server does not offer STARTTLS")
		}
		if err := client.StartTLS(&tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}); err != nil {
			return scrubSecrets(err, cfg.secrets()...)
		}
	}
	if username := strings.TrimSpace(cfg.SMTPUsername); username != "" {
		auth := smtp.PlainAuth("", username, cfg.SMTPPassword, host)
		if err := client.Auth(auth); err != nil {
			return scrubSecrets(err, cfg.secrets()...)
		}
	}
	if err := client.Mail(from.Address); err != nil {
		return scrubSecrets(err, cfg.secrets()...)
	}
	for _, recipient := range recipients {
		if err := client.Rcpt(recipient.Address); err != nil {
			return scrubSecrets(err, cfg.secrets()...)
		}
	}
	writer, err := client.Data()
	if err != nil {
		return scrubSecrets(err, cfg.secrets()...)
	}
	body := buildEmail(from, recipients, title, message)
	if _, err := io.WriteString(writer, body); err != nil {
		_ = writer.Close()
		return scrubSecrets(err, cfg.secrets()...)
	}
	if err := writer.Close(); err != nil {
		return scrubSecrets(err, cfg.secrets()...)
	}
	if err := client.Quit(); err != nil {
		return scrubSecrets(err, cfg.secrets()...)
	}
	return nil
}

func parseRecipients(raw string) ([]*mail.Address, error) {
	parts := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ';'
	})
	var out []*mail.Address
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		address, err := mail.ParseAddress(part)
		if err != nil {
			return nil, fmt.Errorf("invalid recipient address: %w", err)
		}
		out = append(out, address)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no recipient address")
	}
	return out, nil
}

func buildEmail(from *mail.Address, recipients []*mail.Address, title, message string) string {
	to := make([]string, 0, len(recipients))
	for _, recipient := range recipients {
		to = append(to, formatAddress(recipient))
	}
	var b strings.Builder
	fmt.Fprintf(&b, "From: %s\r\n", cleanHeader(formatAddress(from)))
	fmt.Fprintf(&b, "To: %s\r\n", cleanHeader(strings.Join(to, ", ")))
	fmt.Fprintf(&b, "Subject: %s\r\n", mime.QEncoding.Encode("utf-8", cleanHeader(title)))
	fmt.Fprintf(&b, "Date: %s\r\n", time.Now().Format(time.RFC1123Z))
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=UTF-8\r\n")
	b.WriteString("Content-Transfer-Encoding: 8bit\r\n\r\n")
	b.WriteString(crlf(message))
	b.WriteString("\r\n")
	return b.String()
}

func formatAddress(address *mail.Address) string {
	if address == nil {
		return ""
	}
	if strings.TrimSpace(address.Name) == "" {
		return address.Address
	}
	return address.String()
}

func cleanHeader(value string) string {
	value = strings.ReplaceAll(value, "\r", " ")
	return strings.ReplaceAll(value, "\n", " ")
}

func crlf(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	return strings.ReplaceAll(text, "\n", "\r\n")
}

// sendScript runs the executable with a bounded context and passes the event
// through the environment, with both INDEXARR_* and the Prowlarr-style
// PROWLARR_* names so an existing script can be reused.
func sendScript(ctx context.Context, cfg Config, event Event, message string) error {
	path := strings.TrimSpace(cfg.ScriptPath)
	command := exec.CommandContext(ctx, path, splitArgs(cfg.ScriptArgs)...)
	command.Env = mergeEnvironment(os.Environ(), scriptEnvironment(cfg, event, message))
	var stderr limitedBuffer
	command.Stderr = &stderr
	// A killed shell can leave a child holding the stderr pipe open. Bound that
	// wait too, so a misbehaving script cannot outlive the notification.
	command.WaitDelay = 250 * time.Millisecond
	err := command.Run()
	if err == nil {
		return nil
	}
	detail := strings.TrimSpace(stderr.String())
	if detail != "" {
		err = fmt.Errorf("%w: %s", err, detail)
	}
	return scrubSecrets(err, cfg.secrets()...)
}

func scriptEnvironment(cfg Config, event Event, message string) []string {
	instance := strings.TrimSpace(cfg.InstanceName)
	if instance == "" {
		instance = "indexarr"
	}
	baseURL := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	title := grabTitle(event)
	level := healthLevel(event)
	health := strings.TrimSpace(event.Error)
	if health == "" && healthLevel(event) != "info" {
		health = message
	}
	values := map[string]string{
		"EVENTTYPE":         eventType(event.Kind),
		"INSTANCE":          instance,
		"APPLICATIONURL":    baseURL,
		"RELEASETITLE":      title,
		"RELEASESOURCE":     strings.TrimSpace(event.Source),
		"RELEASESIZE":       strconv.FormatInt(event.Size, 10),
		"RELEASECATEGORIES": strings.Join(event.Categories, ","),
		"HEALTHISSUE":       health,
		"HEALTHMESSAGE":     health,
		"HEALTHLEVEL":       level,
		"MESSAGE":           message,
	}
	var env []string
	// Prowlarr scripts read lowercase names; the uppercase form is easier to
	// use in the examples and survives shells that pass the environment as-is.
	// Both spellings carry the same value.
	for _, prefix := range []string{"INDEXARR", "PROWLARR", "indexarr", "prowlarr"} {
		for key, value := range values {
			name := key
			if prefix == "indexarr" || prefix == "prowlarr" {
				name = strings.ToLower(key)
			}
			env = append(env, prefix+"_"+name+"="+value)
		}
		if event.Kind == KindTest {
			env = append(env, prefix+"_TEST=true")
		}
	}
	return env
}

func eventType(kind string) string {
	switch kind {
	case KindNewReleases:
		return "NewReleases"
	case KindIndexerState:
		return "IndexerState"
	case KindGrabFailed:
		return "GrabFailed"
	case KindGrabSuccess:
		return "GrabSuccess"
	case KindCatalogUpdated:
		return "CatalogUpdated"
	case KindBackupFailed:
		return "BackupFailed"
	case KindIndexerDisabled:
		return "IndexerDisabled"
	case KindRunFailed:
		return "RunFailed"
	case KindTest:
		return "Test"
	default:
		return kind
	}
}

func healthLevel(event Event) string {
	switch event.Kind {
	case KindGrabFailed, KindBackupFailed, KindRunFailed:
		return "error"
	case KindIndexerState:
		switch strings.ToLower(strings.TrimSpace(event.Current)) {
		case StateError, StateBlocked:
			return "error"
		case StateDegraded, StateDisabled:
			return "warning"
		}
	case KindIndexerDisabled:
		return "warning"
	}
	return "info"
}

func mergeEnvironment(base, extra []string) []string {
	kept := make([]string, 0, len(base)+len(extra))
	for _, item := range base {
		key, _, _ := strings.Cut(item, "=")
		lower := strings.ToLower(key)
		if strings.HasPrefix(lower, "indexarr_") || strings.HasPrefix(lower, "prowlarr_") {
			continue
		}
		kept = append(kept, item)
	}
	return append(kept, extra...)
}

// splitArgs splits the optional argument string. Single and double quotes keep
// a value with spaces together; a backslash escapes the next character.
func splitArgs(raw string) []string {
	var args []string
	var current strings.Builder
	var quote rune
	escaped := false
	flush := func() {
		if current.Len() > 0 {
			args = append(args, current.String())
			current.Reset()
		}
	}
	for _, r := range raw {
		if escaped {
			current.WriteRune(r)
			escaped = false
			continue
		}
		if r == '\\' && quote != '\'' {
			escaped = true
			continue
		}
		if quote != 0 {
			if r == quote {
				quote = 0
			} else {
				current.WriteRune(r)
			}
			continue
		}
		switch {
		case r == '\'' || r == '"':
			quote = r
		case unicode.IsSpace(r):
			flush()
		default:
			current.WriteRune(r)
		}
	}
	flush()
	return args
}

type limitedBuffer struct {
	data []byte
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	const limit = 8192
	if len(b.data) < limit {
		remaining := limit - len(b.data)
		if len(p) > remaining {
			b.data = append(b.data, p[:remaining]...)
		} else {
			b.data = append(b.data, p...)
		}
	}
	return len(p), nil
}

func (b *limitedBuffer) String() string { return string(b.data) }

func postJSON(ctx context.Context, endpoint string, payload any, headers map[string]string, secrets ...string) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return scrubSecrets(err, secrets...)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", "indexarr")
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	return doRequest(request, secrets...)
}

func postForm(ctx context.Context, endpoint string, values url.Values, headers map[string]string, secrets ...string) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint,
		strings.NewReader(values.Encode()))
	if err != nil {
		return scrubSecrets(err, secrets...)
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("User-Agent", "indexarr")
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	return doRequest(request, secrets...)
}

func doRequest(request *http.Request, secrets ...string) error {
	client := &http.Client{Timeout: DefaultTimeout}
	response, err := client.Do(request)
	if err != nil {
		return scrubSecrets(err, secrets...)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		payload, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return scrubSecrets(fmt.Errorf("HTTP %d: %s", response.StatusCode,
			strings.TrimSpace(string(payload))), secrets...)
	}
	return nil
}

func truncateRunes(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit]) + "…"
}
