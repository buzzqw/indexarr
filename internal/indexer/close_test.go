package indexer

import (
	"context"
	"testing"

	"github.com/buzzqw/indexarr/internal/engine"
)

// closeOnlyConnector stands in for a connector that owns a background queue
// but no browser session.
type closeOnlyConnector struct {
	engine.Connector
	closed bool
}

func (c *closeOnlyConnector) Close() { c.closed = true }

func TestCloseConnectorStopsTheBackgroundQueue(t *testing.T) {
	connector := &closeOnlyConnector{}
	(&Manager{}).closeConnector(context.Background(), connector)
	if !connector.closed {
		t.Fatal("closeConnector did not call Close on the connector")
	}
}
