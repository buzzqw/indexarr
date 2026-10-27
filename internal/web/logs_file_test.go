package web

import (
	"strings"
	"testing"
)

// The Logs page says where the rotating log file is, or that it is off.
func TestLogsPageNamesTheLogFile(t *testing.T) {
	env := newTestEnv(t)
	env.server.logFile, env.server.logMaxMB, env.server.logMaxFiles = "/srv/indexarr/logs/indexarr.log", 10, 5
	body := get(t, env.handler, "/logs?tab=internal&lang=it").Body.String()
	if !strings.Contains(body, "<code title=") || !strings.Contains(body, "/srv/indexarr/logs/indexarr.log</code>") {
		t.Fatalf("log file path missing from the Logs page")
	}
	if !strings.Contains(body, "5 file da massimo 10 MB") {
		t.Errorf("rotation limits missing from the Logs page")
	}
	env.server.logFile = ""
	body = get(t, env.handler, "/logs?tab=internal&lang=en").Body.String()
	if !strings.Contains(body, "The log file on disk is off") {
		t.Errorf("disabled log file not explained")
	}
}
