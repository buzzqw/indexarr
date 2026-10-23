// Package cardigann converts Cardigann YAML definitions (the format used by
// Jackett and Prowlarr) into drafts of the native indexarr definition format
// (v1).
//
// It is a local writing aid, not a migration or import path: it reads a
// definition file, maps the subset of Cardigann that the native format can
// express, and writes a native YAML draft. Every draft carries
// meta.derived_from: cardigann; the built-in catalog rejects derived drafts and
// they must be rewritten by observing the site before they can ship. The
// converter never executes a Cardigann definition, never downloads anything and
// never talks to a tracker. indexarr's runtime continues to interpret only
// native definitions.
//
// Conversion is deliberately explicit about what it cannot represent:
//
//   - features that cannot be mapped at all (captcha, custom Go, multi-step
//     login/download flows, unsupported response types) are reported in
//     Result.Unsupported;
//   - recoverable gaps become Result.Warnings;
//   - request and search templates fail closed: an unknown or dynamic template
//     makes Convert return an error instead of emitting a definition that would
//     send a broken request.
//
// All fixtures in testdata are synthetic and were written for these tests.
package cardigann
