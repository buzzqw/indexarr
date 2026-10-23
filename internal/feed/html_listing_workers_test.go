package feed

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestFetchListingDetailsWorkerCap(t *testing.T) {
	var inFlight atomic.Int64
	var maxInFlight atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		current := inFlight.Add(1)
		for {
			previous := maxInFlight.Load()
			if current <= previous || maxInFlight.CompareAndSwap(previous, current) {
				break
			}
		}
		time.Sleep(15 * time.Millisecond)
		inFlight.Add(-1)
		index, _ := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/detail/"))
		hash := fmt.Sprintf("%040x", index+1)
		_, _ = fmt.Fprintf(w, `<html><body><a href="magnet:?xt=urn:btih:%s&dn=detail">magnet</a></body></html>`, hash)
	}))
	defer server.Close()

	const details = 10
	refs := make([]listingDetail, 0, details)
	for i := 0; i < details; i++ {
		refs = append(refs, listingDetail{
			title: fmt.Sprintf("Detail %d", i),
			url:   fmt.Sprintf("%s/detail/%d", server.URL, i),
		})
	}
	items, err := fetchListingDetails(context.Background(), server.Client(), refs, RequestOptions{}, "")
	if err != nil {
		t.Fatalf("fetchListingDetails: %v", err)
	}
	if len(items) != details {
		t.Errorf("items = %d, want %d", len(items), details)
	}
	if got := maxInFlight.Load(); got > 2 {
		t.Errorf("detail workers in flight = %d, want at most 2", got)
	}
}
