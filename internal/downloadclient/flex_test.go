package downloadclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestFlexNumbersAcceptStringsAndNull(t *testing.T) {
	var value struct {
		Int   flexInt   `json:"i"`
		Float flexFloat `json:"f"`
		Empty flexInt   `json:"e"`
	}
	if err := json.Unmarshal([]byte(`{"i":"42","f":"0.5","e":null}`), &value); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if value.Int != 42 || value.Float != 0.5 || value.Empty != 0 {
		t.Fatalf("got int=%v float=%v empty=%v", value.Int, value.Float, value.Empty)
	}
}

func TestQBStateUsesOneWordForPaused(t *testing.T) {
	for _, state := range []string{"pausedDL", "pausedUP", "stoppedDL", "stoppedUP"} {
		if got := qbState(state); got != "paused" {
			t.Errorf("qbState(%q) = %q, want paused", state, got)
		}
	}
	if got := qbState("uploading"); got != "uploading" {
		t.Errorf("qbState(uploading) = %q", got)
	}
}

// TestQBittorrentQueueNormalizesFiveX keeps the read-only queue usable after
// the 5.x rename: numbers arrive as strings and paused states as stopped*.
func TestQBittorrentQueueNormalizesFiveX(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/torrents/info":
			_, _ = w.Write([]byte(`[{"hash":"abc","name":"Release","size":"1000","total_size":"2000",
				"amount_left":"500","downloaded":"1500","progress":"0.75","dlspeed":"10","upspeed":"2",
				"eta":"60","state":"stoppedDL"}]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	items, err := queueQBittorrent(context.Background(), Config{
		Kind: "qbittorrent", URL: server.URL, Username: "user", Password: "pass",
	})
	if err != nil {
		t.Fatalf("queue: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("items = %d", len(items))
	}
	item := items[0]
	if item.Size != 2000 || item.Done != 1500 || item.Progress != 0.75 {
		t.Errorf("size=%d done=%d progress=%v", item.Size, item.Done, item.Progress)
	}
	if item.SpeedDown != 10 || item.SpeedUp != 2 || item.ETA != time.Minute {
		t.Errorf("down=%d up=%d eta=%v", item.SpeedDown, item.SpeedUp, item.ETA)
	}
	if item.State != "paused" {
		t.Errorf("state = %q, want paused", item.State)
	}
}
