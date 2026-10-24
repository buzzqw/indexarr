//go:build race

package web

import "time"

// The race detector adds enough instrumentation to make a 50 ms UI budget
// unreliable; keep the normal budget strict and give the race build room.
const catalogIndexBudget = 250 * time.Millisecond
