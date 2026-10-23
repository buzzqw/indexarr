package compat

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
)

func TestSearchGolden(t *testing.T) {
	e := newEnv(t)
	recorder := e.get("/api?t=search&apikey=" + primaryKey + "&q=" + searchQuery + "&limit=100")
	assertWellFormedXML(t, recorder.Body.Bytes())
	assertGolden(t, recorder.Body.Bytes(), "search.golden.xml")
}

func TestConcurrentSearches(t *testing.T) {
	e := newEnv(t)
	server := httptest.NewServer(e.server)
	defer server.Close()

	const workers = 20
	var wait sync.WaitGroup
	errs := make(chan string, workers)
	for i := 0; i < workers; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			response, err := server.Client().Get(server.URL + "/api?t=search&apikey=" +
				primaryKey + "&q=" + searchQuery + "&limit=100")
			if err != nil {
				errs <- err.Error()
				return
			}
			body, readErr := io.ReadAll(io.LimitReader(response.Body, 1<<20))
			response.Body.Close()
			if readErr != nil {
				errs <- readErr.Error()
				return
			}
			if response.StatusCode != http.StatusOK {
				errs <- "status " + response.Status + ": " + string(body)
				return
			}
			if len(body) == 0 || len(body) > 64<<10 {
				errs <- "unbounded or empty response, bytes=" + strconv.Itoa(len(body))
				return
			}
			if !strings.Contains(string(body), "<newznab:response") {
				errs <- "response is not a Torznab document: " + string(body)
			}
		}()
	}
	wait.Wait()
	close(errs)
	for failure := range errs {
		t.Errorf("concurrent search: %s", failure)
	}
}
