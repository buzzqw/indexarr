package logfile

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestRotatesBySizeAndKeepsTheConfiguredFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "logs", "indexarr.log")
	r, err := Open(path, 100, 3)
	if err != nil {
		t.Fatal(err)
	}
	line := []byte(strings.Repeat("x", 39) + "\n") // 40 bytes: two lines per file
	for i := 0; i < 10; i++ {
		if _, err := r.Write(line); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"indexarr.log", "indexarr.log.1", "indexarr.log.2"} {
		data, err := os.ReadFile(filepath.Join(dir, "logs", name))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(data) != 80 {
			t.Errorf("%s = %d bytes, want 80", name, len(data))
		}
	}
	if _, err := os.Stat(path + ".3"); !os.IsNotExist(err) {
		t.Errorf("a fourth file was kept: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", info.Mode().Perm())
	}
}

func TestReopenAppendsAndCountsTheExistingSize(t *testing.T) {
	path := filepath.Join(t.TempDir(), "indexarr.log")
	r, err := Open(path, 100, 2)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = r.Write(bytes.Repeat([]byte("a"), 60))
	_ = r.Close()
	r, err = Open(path, 100, 2)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = r.Write(bytes.Repeat([]byte("b"), 60)) // 120 > 100: rotate first
	_ = r.Close()
	old, _ := os.ReadFile(path + ".1")
	current, _ := os.ReadFile(path)
	if string(old) != strings.Repeat("a", 60) || string(current) != strings.Repeat("b", 60) {
		t.Fatalf("old = %q, current = %q", old, current)
	}
}

func TestConcurrentWritesStayWhole(t *testing.T) {
	path := filepath.Join(t.TempDir(), "indexarr.log")
	r, err := Open(path, 1000, 5)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				_, _ = r.Write([]byte("record-0123456789\n"))
			}
		}()
	}
	wg.Wait()
	_ = r.Close()
	matches, _ := filepath.Glob(path + "*")
	for _, name := range matches {
		data, _ := os.ReadFile(name)
		for _, line := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
			if line != "record-0123456789" {
				t.Fatalf("%s holds a split record %q", name, line)
			}
		}
	}
	if _, err := r.Write([]byte("late\n")); err == nil {
		t.Fatal("write after Close succeeded")
	}
}
