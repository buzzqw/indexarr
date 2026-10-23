// Package engine is the indexarr 2 kernel: the connector and pipeline
// contracts plus the HTTP transport shared by every connector. The transport
// enforces SSRF protection, per-host rate limiting, body size limits and a
// per-indexer circuit breaker.
//
// The package depends only on the standard library. Definitions, persistence
// and the concrete connectors plug in from other packages; keeping this
// boundary lets the kernel evolve without import cycles.
package engine
