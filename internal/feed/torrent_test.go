package feed

import (
	"context"
	"crypto/sha1"
	"encoding/base32"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// testTorrent is a minimal valid bencoded torrent. The infohash is the SHA-1
// of the exact "info" dictionary bytes.
func testTorrent() []byte {
	info := "d6:lengthi12345e4:name8:test.iso12:piece lengthi16384e6:pieces20:aaaaaaaaaaaaaaaaaaaa" + "e"
	return []byte("d8:announce14:http://tracker4:info" + info + "e")
}

func TestInfoHash(t *testing.T) {
	data := testTorrent()
	info := data[strings.Index(string(data), "4:info")+6 : len(data)-1]
	want := sha1.Sum(info)
	got, err := InfoHash(data)
	if err != nil {
		t.Fatalf("InfoHash: %v", err)
	}
	if got != hex.EncodeToString(want[:]) {
		t.Errorf("infohash = %s, want %s", got, hex.EncodeToString(want[:]))
	}
}

func TestInfoHashRejectsGarbage(t *testing.T) {
	for _, input := range []string{"", "not a torrent", "<html></html>", "d8:announce3:urle"} {
		if _, err := InfoHash([]byte(input)); err == nil {
			t.Errorf("InfoHash(%q) succeeded, want error", input)
		}
	}
}

func TestMagnetHashForms(t *testing.T) {
	hash := "0123456789abcdef0123456789abcdef01234567"
	raw, err := hex.DecodeString(hash)
	if err != nil {
		t.Fatal(err)
	}
	base32Hash := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(raw)

	if got := NormalizeInfoHash(hash); got != hash {
		t.Errorf("hex normalization = %q", got)
	}
	if got := NormalizeInfoHash(strings.ToUpper(hash)); got != hash {
		t.Errorf("uppercase normalization = %q", got)
	}
	if got := NormalizeInfoHash(base32Hash); got != hash {
		t.Errorf("base32 normalization = %q", got)
	}
	for _, magnet := range []string{
		"magnet:?xt=urn:btih:" + hash,
		"magnet:?xt=urn:btih:" + strings.ToUpper(hash) + "&dn=Name",
		"magnet:?xt=urn:btih:" + base32Hash + "&dn=Name",
	} {
		if got := MagnetHash(magnet); got != hash {
			t.Errorf("MagnetHash(%q) = %q", magnet, got)
		}
	}
	if got := MagnetHash("https://example.test/file.torrent"); got != "" {
		t.Errorf("MagnetHash(http) = %q, want empty", got)
	}
	if got := MagnetFromHash(hash, "Some Name"); !strings.Contains(got, "dn=Some+Name") {
		t.Errorf("MagnetFromHash = %q", got)
	}
}

func TestFetchTorrent(t *testing.T) {
	data := testTorrent()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("apikey") != "secret" {
			t.Errorf("apikey = %q", r.URL.Query().Get("apikey"))
		}
		w.Header().Set("Content-Type", "application/x-bittorrent")
		_, _ = w.Write(data)
	}))
	defer server.Close()

	body, hash, err := FetchTorrent(context.Background(), server.Client(), server.URL, TorrentRequest{
		APIKey:    "secret",
		UserAgent: "test-agent",
		MaxBytes:  1 << 20,
	})
	if err != nil {
		t.Fatalf("FetchTorrent: %v", err)
	}
	if string(body) != string(data) {
		t.Error("body differs")
	}
	want, _ := InfoHash(data)
	if hash != want {
		t.Errorf("hash = %s, want %s", hash, want)
	}
}

func TestFetchTorrentSizeLimit(t *testing.T) {
	data := testTorrent()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(data)
	}))
	defer server.Close()
	if _, _, err := FetchTorrent(context.Background(), server.Client(), server.URL, TorrentRequest{MaxBytes: 10}); err == nil {
		t.Fatal("expected the size limit to reject the torrent")
	}
}

func TestFetchTorrentRejectsHTML(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("<html>login</html>"))
	}))
	defer server.Close()
	_, _, err := FetchTorrent(context.Background(), server.Client(), server.URL, TorrentRequest{MaxBytes: 1 << 20})
	if err == nil {
		t.Fatal("expected a non-torrent body to be rejected")
	}
}

func TestFetchTorrentMagnetRedirect(t *testing.T) {
	magnet := "magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567&dn=Show"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, magnet, http.StatusFound)
	}))
	defer server.Close()

	_, _, err := FetchTorrent(context.Background(), server.Client(), server.URL, TorrentRequest{})
	var magnetErr *MagnetRedirectError
	if !errors.As(err, &magnetErr) {
		t.Fatalf("FetchTorrent error = %v, want MagnetRedirectError", err)
	}
	if magnetErr.Magnet != magnet {
		t.Errorf("magnet = %q", magnetErr.Magnet)
	}
}

func TestParseTorrentMetadata(t *testing.T) {
	meta, err := ParseTorrent(testTorrent())
	if err != nil {
		t.Fatalf("ParseTorrent: %v", err)
	}
	if meta.Name != "test.iso" || meta.Size != 12345 || meta.InfoHash == "" {
		t.Fatalf("meta = %+v", meta)
	}
	if _, err := ParseTorrent([]byte("<html>login</html>")); err == nil {
		t.Fatal("HTML accepted as a torrent")
	}
}

func TestParseTorrentFileList(t *testing.T) {
	single, err := ParseTorrent(testTorrent())
	if err != nil {
		t.Fatalf("ParseTorrent single: %v", err)
	}
	if len(single.Files) != 1 || single.Files[0].Path != "test.iso" || single.Files[0].Size != 12345 {
		t.Fatalf("single files = %+v", single.Files)
	}

	multi := []byte("d4:infod5:filesl" +
		"d6:lengthi100e4:pathl5:a.mkvee" +
		"d6:lengthi200e4:pathl5:b.mkvee" +
		"e4:name3:foo" +
		"12:piece lengthi16384e6:pieces0:ee")
	meta, err := ParseTorrent(multi)
	if err != nil {
		t.Fatalf("ParseTorrent multi: %v", err)
	}
	if len(meta.Files) != 2 || meta.Files[0].Path != "a.mkv" || meta.Files[1].Path != "b.mkv" {
		t.Fatalf("multi files = %+v", meta.Files)
	}
	if meta.Size != 300 {
		t.Fatalf("multi size = %d", meta.Size)
	}
}
