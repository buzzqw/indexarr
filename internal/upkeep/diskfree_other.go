//go:build !unix

package upkeep

// freeBytes is unknown on this platform: the routine then trusts the size
// check of the compaction step alone.
func freeBytes(string) int64 { return -1 }
