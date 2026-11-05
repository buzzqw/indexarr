package downloadclient

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestQBittorrentQueueReadsProgress(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/torrents/info":
			_, _ = w.Write([]byte(`[{"hash":"abc","name":"Test Item","size":1048576,"total_size":1048576,"progress":0.5,"amount_left":524288,"downloaded":524288,"dlspeed":1024,"upspeed":128,"eta":60,"state":"downloading"}]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	items, err := Queue(context.Background(), Config{Kind: KindQBittorrent, URL: server.URL})
	if err != nil {
		t.Fatalf("Queue: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("items = %+v", items)
	}
	item := items[0]
	if item.Name != "Test Item" || item.Hash != "abc" || item.Size != 1048576 ||
		item.Done != 524288 || item.Progress != 0.5 || item.SpeedDown != 1024 ||
		item.SpeedUp != 128 || item.ETA != time.Minute || item.State != "downloading" {
		t.Errorf("item = %+v", item)
	}
}

func TestTransmissionQueueReadsProgress(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/transmission/rpc" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("X-Transmission-Session-Id") == "" {
			w.Header().Set("X-Transmission-Session-Id", "sid")
			http.Error(w, "conflict", http.StatusConflict)
			return
		}
		_, _ = w.Write([]byte(`{"result":"success","arguments":{"torrents":[{"hashString":"def","name":"Transmission Item","totalSize":2048,"percentDone":0.25,"rateDownload":512,"rateUpload":64,"eta":120,"status":4,"leftUntilDone":1536}]}}`))
	}))
	defer server.Close()

	items, err := Queue(context.Background(), Config{Kind: KindTransmission, URL: server.URL})
	if err != nil {
		t.Fatalf("Queue: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("items = %+v", items)
	}
	item := items[0]
	if item.Name != "Transmission Item" || item.Size != 2048 || item.Done != 512 ||
		item.Progress != 0.25 || item.SpeedDown != 512 || item.ETA != 2*time.Minute ||
		item.State != "downloading" {
		t.Errorf("item = %+v", item)
	}
}

func TestSabnzbdQueueReadsProgress(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"queue":{"slots":[{"nzo_id":"sab1","filename":"SAB Item","percentage":"50.0","mb":"10.0","mbleft":"5.0","timeleft":"0:01:30","status":"Downloading"}]}}`))
	}))
	defer server.Close()

	items, err := Queue(context.Background(), Config{Kind: KindSABnzbd, URL: server.URL, APIKey: "key"})
	if err != nil {
		t.Fatalf("Queue: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("items = %+v", items)
	}
	item := items[0]
	if item.Name != "SAB Item" || item.Size != 10*1024*1024 || item.Done != 5*1024*1024 ||
		item.Progress != 0.5 || item.ETA != 90*time.Second || item.State != "Downloading" {
		t.Errorf("item = %+v", item)
	}
}

func TestNZBGetQueueReadsProgress(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"result":[{"NZBID":7,"NZBName":"NZBGet Item","FileSizeMB":20,"RemainingSizeMB":5,"Status":"DOWNLOADING","DownloadRate":2048}]}`))
	}))
	defer server.Close()

	items, err := Queue(context.Background(), Config{Kind: KindNZBGet, URL: server.URL})
	if err != nil {
		t.Fatalf("Queue: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("items = %+v", items)
	}
	item := items[0]
	if item.Name != "NZBGet Item" || item.Size != 20*1024*1024 || item.Done != 15*1024*1024 ||
		item.Progress != 0.75 || item.SpeedDown != 2048 || item.State != "DOWNLOADING" {
		t.Errorf("item = %+v", item)
	}
}

func TestQueueUnsupportedClient(t *testing.T) {
	_, err := Queue(context.Background(), Config{Kind: KindRTorrent, URL: "http://127.0.0.1:8080/RPC2"})
	if !errors.Is(err, ErrQueueUnsupported) {
		t.Errorf("error = %v, want ErrQueueUnsupported", err)
	}
}

func TestDelugeQueueReadsProgress(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&request)
		switch request.Method {
		case "auth.login":
			_, _ = w.Write([]byte(`{"result":true}`))
		case "web.update_ui":
			_, _ = w.Write([]byte(`{"result":{"torrents":{"hash1":{"name":"Deluge Item","total_size":4096,"progress":50.0,"download_rate":256,"upload_rate":32,"eta":90,"state":"Downloading"}}}}`))
		default:
			_, _ = w.Write([]byte(`{"result":true}`))
		}
	}))
	defer server.Close()

	items, err := Queue(context.Background(), Config{Kind: KindDeluge, URL: server.URL, Password: "secret"})
	if err != nil {
		t.Fatalf("Queue: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("items = %+v", items)
	}
	item := items[0]
	if item.Name != "Deluge Item" || item.Size != 4096 || item.Progress != 0.5 ||
		item.Done != 2048 || item.SpeedDown != 256 || item.ETA != 90*time.Second ||
		item.State != "Downloading" {
		t.Errorf("item = %+v", item)
	}
}
