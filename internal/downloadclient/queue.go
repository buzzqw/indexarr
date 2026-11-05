package downloadclient

import (
	"context"
	"errors"
	"time"
)

// ErrQueueUnsupported is returned for a client whose API does not expose a
// queue indexarr can read. The page shows a note instead of treating it as an
// outage.
var ErrQueueUnsupported = errors.New("this download client does not expose a queue")

// QueueItem is one download in a client's queue. Progress is 0..1; Done is the
// bytes already transferred and Size the total the client reports.
type QueueItem struct {
	Name      string
	Hash      string
	Size      int64
	Done      int64
	Progress  float64
	SpeedDown int64
	SpeedUp   int64
	ETA       time.Duration
	State     string
}

// QueueReader is implemented by the clients whose API indexarr can read
// without changing anything.
type QueueReader interface {
	Queue(ctx context.Context) ([]QueueItem, error)
}

// Queue reads the queue of the configured client.
func Queue(ctx context.Context, cfg Config) ([]QueueItem, error) {
	client, err := New(cfg)
	if err != nil {
		return nil, err
	}
	reader, ok := client.(QueueReader)
	if !ok {
		return nil, ErrQueueUnsupported
	}
	return reader.Queue(ctx)
}
