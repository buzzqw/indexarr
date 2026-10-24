//go:build !race

package web

import "time"

const catalogIndexBudget = 50 * time.Millisecond
