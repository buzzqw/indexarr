package store

import (
	"testing"
	"time"
)

func TestQuarantineDurationGrows(t *testing.T) {
	cases := map[int64]time.Duration{
		0: 0,
		1: 0,
		2: 6 * time.Hour,
		3: 24 * time.Hour,
		4: 7 * 24 * time.Hour,
		5: 7 * 24 * time.Hour,
	}
	for failures, want := range cases {
		if got := QuarantineDuration(failures); got != want {
			t.Errorf("QuarantineDuration(%d) = %v, want %v", failures, got, want)
		}
	}
}
