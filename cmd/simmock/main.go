// Command simmock runs the deterministic fake services used by the indexarr
// end-to-end simulation.
package main

import (
	"flag"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/buzzqw/indexarr/internal/simmock"
)

func main() {
	defaultAddr := strings.TrimSpace(os.Getenv("SIMM_MOCK_ADDR"))
	if defaultAddr == "" {
		defaultAddr = ":19080"
	}

	addr := flag.String("addr", defaultAddr, "HTTP listen address")
	apiKey := flag.String("apikey", "demo-key", "Torznab/Newznab API key")
	sonarrKey := flag.String("sonarr-key", "demo-sonarr", "Sonarr API key")
	prowlarrKey := flag.String("prowlarr-key", "demo-prowlarr", "Prowlarr API key")
	flag.Parse()

	logger := log.New(os.Stderr, "simmock ", log.LstdFlags)
	server := simmock.New(simmock.Config{
		APIKey:      *apiKey,
		SonarrKey:   *sonarrKey,
		ProwlarrKey: *prowlarrKey,
		Logger:      logger,
	})

	httpServer := &http.Server{
		Addr:              *addr,
		Handler:           server.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	logger.Printf("listening on %s apikey=%s sonarr-key=%s prowlarr-key=%s",
		*addr, *apiKey, *sonarrKey, *prowlarrKey)
	if err := httpServer.ListenAndServe(); err != nil {
		logger.Fatalf("server stopped: %v", err)
	}
}
