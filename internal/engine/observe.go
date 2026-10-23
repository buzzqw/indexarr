package engine

import (
	"context"
	"time"
)

// ResponseObservation is one HTTP response seen by the shared transport. The
// body is the response prefix the observer asked for; it is never logged and
// it may contain tracker credentials. The caller is responsible for redacting
// it before storage.
type ResponseObservation struct {
	Method   string
	URL      string
	Status   int
	Duration time.Duration
	Body     []byte
}

// NormalizeObservation reports whether one extracted row became a normalized
// item. It lets a run count parse failures without a second pass over the raw
// response and without re-running the extractor.
type NormalizeObservation struct {
	OK bool
}

type responseObserverKey struct{}
type normalizeObserverKey struct{}

// WithResponseObserver attaches fn to ctx. The transport calls it for every
// response it reads, including errors returned as responses. It is scoped to
// the context, so two concurrent runs never share observations.
func WithResponseObserver(ctx context.Context, fn func(ResponseObservation)) context.Context {
	if fn == nil {
		return ctx
	}
	return context.WithValue(ctx, responseObserverKey{}, fn)
}

// WithNormalizeObserver attaches fn to ctx. Definition connectors call it once
// per extracted row, before the row is appended to the result.
func WithNormalizeObserver(ctx context.Context, fn func(NormalizeObservation)) context.Context {
	if fn == nil {
		return ctx
	}
	return context.WithValue(ctx, normalizeObserverKey{}, fn)
}

func observeResponse(ctx context.Context, observation ResponseObservation) {
	if ctx == nil {
		return
	}
	if fn, ok := ctx.Value(responseObserverKey{}).(func(ResponseObservation)); ok && fn != nil {
		fn(observation)
	}
}

func observeNormalize(ctx context.Context, observation NormalizeObservation) {
	if ctx == nil {
		return
	}
	if fn, ok := ctx.Value(normalizeObserverKey{}).(func(NormalizeObservation)); ok && fn != nil {
		fn(observation)
	}
}
