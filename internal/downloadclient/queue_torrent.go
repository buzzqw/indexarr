package downloadclient

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"time"
)

func queueQBittorrent(ctx context.Context, cfg Config) ([]QueueItem, error) {
	base := strings.TrimRight(strings.TrimSpace(cfg.URL), "/")
	if base == "" {
		return nil, fmt.Errorf("qBittorrent URL is empty")
	}
	jar, _ := cookiejar.New(nil)
	client := newHTTPClient(jar)
	if err := qbLogin(ctx, client, base, cfg); err != nil {
		return nil, err
	}
	items, err := qbQueue(ctx, client, base, cfg)
	if err != nil {
		return nil, err
	}
	return items, nil
}

func qbQueue(ctx context.Context, client *http.Client, base string, cfg Config) ([]QueueItem, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet,
		base+"/api/v2/torrents/info", nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Referer", base)
	response, err := client.Do(request)
	if err != nil {
		return nil, requestError("qBittorrent queue", err, base, cfg.Password)
	}
	body, _ := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("qBittorrent queue failed (HTTP %d): %s",
			response.StatusCode, redactText(string(body), maxErrorBody, cfg.Password))
	}
	var rows []struct {
		Hash       string  `json:"hash"`
		Name       string  `json:"name"`
		Size       int64   `json:"size"`
		TotalSize  int64   `json:"total_size"`
		Progress   float64 `json:"progress"`
		AmountLeft int64   `json:"amount_left"`
		Downloaded int64   `json:"downloaded"`
		DLRate     int64   `json:"dlspeed"`
		ULRate     int64   `json:"upspeed"`
		ETA        int64   `json:"eta"`
		State      string  `json:"state"`
		Completion int64   `json:"completion_on"`
	}
	if err := json.Unmarshal(body, &rows); err != nil {
		return nil, fmt.Errorf("qBittorrent queue response: %w", err)
	}
	items := make([]QueueItem, 0, len(rows))
	for _, row := range rows {
		total := row.TotalSize
		if total <= 0 {
			total = row.Size
		}
		done := row.Downloaded
		if row.AmountLeft >= 0 && total > 0 {
			done = total - row.AmountLeft
		}
		if done < 0 {
			done = 0
		}
		if total > 0 && done > total {
			done = total
		}
		eta := time.Duration(0)
		if row.ETA > 0 && row.ETA < 8640000 {
			eta = time.Duration(row.ETA) * time.Second
		}
		items = append(items, QueueItem{
			Name:      row.Name,
			Hash:      row.Hash,
			Size:      total,
			Done:      done,
			Progress:  clampProgress(row.Progress),
			SpeedDown: row.DLRate,
			SpeedUp:   row.ULRate,
			ETA:       eta,
			State:     row.State,
		})
	}
	return items, nil
}

func queueTransmission(ctx context.Context, cfg Config) ([]QueueItem, error) {
	endpoint, err := transmissionEndpoint(cfg.URL)
	if err != nil {
		return nil, err
	}
	raw, err := transmissionCall(ctx, cfg, endpoint, "torrent-get", map[string]any{
		"fields": []string{
			"hashString", "name", "totalSize", "percentDone",
			"rateDownload", "rateUpload", "eta", "status", "leftUntilDone",
		},
	})
	if err != nil {
		return nil, err
	}
	var result struct {
		Torrents []struct {
			Hash          string  `json:"hashString"`
			Name          string  `json:"name"`
			TotalSize     int64   `json:"totalSize"`
			PercentDone   float64 `json:"percentDone"`
			RateDownload  int64   `json:"rateDownload"`
			RateUpload    int64   `json:"rateUpload"`
			ETA           int64   `json:"eta"`
			Status        int     `json:"status"`
			LeftUntilDone int64   `json:"leftUntilDone"`
		} `json:"torrents"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("Transmission queue response: %w", err)
	}
	items := make([]QueueItem, 0, len(result.Torrents))
	for _, row := range result.Torrents {
		done := row.TotalSize - row.LeftUntilDone
		if done < 0 {
			done = 0
		}
		if done > row.TotalSize {
			done = row.TotalSize
		}
		eta := time.Duration(0)
		if row.ETA > 0 {
			eta = time.Duration(row.ETA) * time.Second
		}
		items = append(items, QueueItem{
			Name:      row.Name,
			Hash:      row.Hash,
			Size:      row.TotalSize,
			Done:      done,
			Progress:  clampProgress(row.PercentDone),
			SpeedDown: row.RateDownload,
			SpeedUp:   row.RateUpload,
			ETA:       eta,
			State:     transmissionState(row.Status),
		})
	}
	return items, nil
}

func transmissionState(status int) string {
	switch status {
	case 0:
		return "stopped"
	case 1:
		return "check wait"
	case 2:
		return "checking"
	case 3:
		return "download wait"
	case 4:
		return "downloading"
	case 5:
		return "seed wait"
	case 6:
		return "seeding"
	default:
		return fmt.Sprintf("status %d", status)
	}
}

func clampProgress(value float64) float64 {
	if value < 0 {
		return 0
	}
	if value > 1 {
		return 1
	}
	return value
}
