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
		Hash       string    `json:"hash"`
		Name       string    `json:"name"`
		Size       flexInt   `json:"size"`
		TotalSize  flexInt   `json:"total_size"`
		Progress   flexFloat `json:"progress"`
		AmountLeft flexInt   `json:"amount_left"`
		Downloaded flexInt   `json:"downloaded"`
		DLRate     flexInt   `json:"dlspeed"`
		ULRate     flexInt   `json:"upspeed"`
		ETA        flexInt   `json:"eta"`
		State      string    `json:"state"`
		Completion flexInt   `json:"completion_on"`
	}
	if err := json.Unmarshal(body, &rows); err != nil {
		return nil, fmt.Errorf("qBittorrent queue response: %w", err)
	}
	items := make([]QueueItem, 0, len(rows))
	for _, row := range rows {
		total := int64(row.TotalSize)
		if total <= 0 {
			total = int64(row.Size)
		}
		done := int64(row.Downloaded)
		if row.AmountLeft >= 0 && total > 0 {
			done = total - int64(row.AmountLeft)
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
			Progress:  clampProgress(float64(row.Progress)),
			SpeedDown: int64(row.DLRate),
			SpeedUp:   int64(row.ULRate),
			ETA:       eta,
			State:     qbState(row.State),
		})
	}
	return items, nil
}

// qbState normalizes the qBittorrent names. The Web API renamed the paused
// states to stoppedDL/stoppedUP in 5.x; the queue shows the same word for both
// generations so a client upgrade does not change what the page says.
func qbState(state string) string {
	switch strings.TrimSpace(state) {
	case "pausedDL", "pausedUP", "stoppedDL", "stoppedUP":
		return "paused"
	default:
		return state
	}
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
			Hash          string    `json:"hashString"`
			Name          string    `json:"name"`
			TotalSize     flexInt   `json:"totalSize"`
			PercentDone   flexFloat `json:"percentDone"`
			RateDownload  flexInt   `json:"rateDownload"`
			RateUpload    flexInt   `json:"rateUpload"`
			ETA           flexInt   `json:"eta"`
			Status        flexInt   `json:"status"`
			LeftUntilDone flexInt   `json:"leftUntilDone"`
		} `json:"torrents"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("Transmission queue response: %w", err)
	}
	items := make([]QueueItem, 0, len(result.Torrents))
	for _, row := range result.Torrents {
		size := int64(row.TotalSize)
		done := size - int64(row.LeftUntilDone)
		if done < 0 {
			done = 0
		}
		if done > size {
			done = size
		}
		eta := time.Duration(0)
		if row.ETA > 0 {
			eta = time.Duration(row.ETA) * time.Second
		}
		items = append(items, QueueItem{
			Name:      row.Name,
			Hash:      row.Hash,
			Size:      size,
			Done:      done,
			Progress:  clampProgress(float64(row.PercentDone)),
			SpeedDown: int64(row.RateDownload),
			SpeedUp:   int64(row.RateUpload),
			ETA:       eta,
			State:     transmissionState(int(row.Status)),
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
