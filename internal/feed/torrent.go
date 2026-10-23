package feed

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// InfoHash computes the BitTorrent infohash of a .torrent file: the SHA-1 of
// the bencoded "info" dictionary, taken byte for byte as it appears in the
// file.
func InfoHash(data []byte) (string, error) {
	if len(data) < 2 || data[0] != 'd' {
		return "", errors.New("not a bencoded dictionary")
	}
	pos := 1
	for pos < len(data) && data[pos] != 'e' {
		key, next, err := parseBencodeString(data, pos)
		if err != nil {
			return "", err
		}
		valueStart := next
		valueEnd, err := parseBencodeValue(data, valueStart)
		if err != nil {
			return "", err
		}
		if string(key) == "info" {
			if data[valueStart] != 'd' {
				return "", errors.New("torrent info is not a dictionary")
			}
			sum := sha1.Sum(data[valueStart:valueEnd])
			return hex.EncodeToString(sum[:]), nil
		}
		pos = valueEnd
	}
	return "", errors.New("torrent has no info dictionary")
}

func parseBencodeString(data []byte, pos int) ([]byte, int, error) {
	colon := pos
	for colon < len(data) && data[colon] != ':' {
		if data[colon] < '0' || data[colon] > '9' {
			return nil, 0, fmt.Errorf("invalid bencode string at byte %d", pos)
		}
		colon++
	}
	if colon >= len(data) {
		return nil, 0, errors.New("truncated bencode string")
	}
	length := 0
	for _, r := range data[pos:colon] {
		length = length*10 + int(r-'0')
		if length > len(data) {
			return nil, 0, errors.New("bencode string longer than the file")
		}
	}
	start := colon + 1
	end := start + length
	if end > len(data) {
		return nil, 0, errors.New("truncated bencode string")
	}
	return data[start:end], end, nil
}

func parseBencodeValue(data []byte, pos int) (int, error) {
	if pos >= len(data) {
		return 0, errors.New("truncated bencode value")
	}
	switch data[pos] {
	case 'i':
		end := pos + 1
		for end < len(data) && data[end] != 'e' {
			end++
		}
		if end >= len(data) {
			return 0, errors.New("truncated bencode integer")
		}
		return end + 1, nil
	case 'l', 'd':
		end := pos + 1
		for end < len(data) && data[end] != 'e' {
			var err error
			if data[pos] == 'd' {
				_, end, err = parseBencodeString(data, end)
				if err != nil {
					return 0, err
				}
			}
			end, err = parseBencodeValue(data, end)
			if err != nil {
				return 0, err
			}
		}
		if end >= len(data) {
			return 0, errors.New("truncated bencode container")
		}
		return end + 1, nil
	default:
		_, end, err := parseBencodeString(data, pos)
		return end, err
	}
}

// TorrentRequest describes one .torrent download.
type TorrentRequest struct {
	APIKey    string
	UserAgent string
	Username  string
	Password  string
	MaxBytes  int64
}

// FetchTorrent downloads a .torrent file with a hard size limit and returns
// its bytes together with the infohash.
func FetchTorrent(ctx context.Context, client *http.Client, rawURL string, opts TorrentRequest) ([]byte, string, error) {
	if client == nil {
		client = http.DefaultClient
	}
	rawURL = strings.TrimSpace(rawURL)
	parsed, err := url.Parse(rawURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return nil, "", fmt.Errorf("invalid torrent URL %q", rawURL)
	}
	sourceURL := parsed.String()
	if opts.APIKey != "" {
		query := parsed.Query()
		if query.Get("apikey") == "" && query.Get("passkey") == "" {
			query.Set("apikey", opts.APIKey)
			parsed.RawQuery = query.Encode()
			sourceURL = parsed.String()
		}
	}
	request, err := newRequest(ctx, sourceURL, RequestOptions{
		UserAgent: opts.UserAgent,
		APIKey:    opts.APIKey,
		Username:  opts.Username,
		Password:  opts.Password,
	})
	if err != nil {
		return nil, "", err
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, "", fmt.Errorf("%s", redactSecrets(err.Error()))
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 64<<10))
		return nil, "", describeHTTPError(response.StatusCode, body)
	}
	maxBytes := opts.MaxBytes
	if maxBytes <= 0 {
		maxBytes = 8 << 20
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxBytes+1))
	if err != nil {
		return nil, "", err
	}
	if int64(len(body)) > maxBytes {
		return nil, "", fmt.Errorf("torrent larger than %d bytes", maxBytes)
	}
	hash, err := InfoHash(body)
	if err != nil {
		return nil, "", fmt.Errorf("downloaded file is not a torrent: %w", err)
	}
	return body, hash, nil
}

// resolveRedirectMagnet follows at most one redirect from a manager proxy URL
// and returns the magnet it points to, when there is one.
func resolveRedirectMagnet(ctx context.Context, client *http.Client, rawURL string, opts RequestOptions) string {
	if client == nil || !strings.HasPrefix(strings.ToLower(rawURL), "http") {
		return ""
	}
	probe := *client
	probe.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	request, err := newRequest(ctx, rawURL, opts)
	if err != nil {
		return ""
	}
	response, err := probe.Do(request)
	if err != nil {
		return ""
	}
	_ = response.Body.Close()
	location := response.Header.Get("Location")
	if strings.HasPrefix(strings.ToLower(location), "magnet:") {
		return location
	}
	return ""
}
