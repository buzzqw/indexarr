package cardigann

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// TestUIPackagesDoNotImportCardigann is an architecture guard: the web UI and
// the admin API must never expose the Cardigann converter. It stays a CLI-only
// writing aid, so no route can import a converted draft or run a conversion.
func TestUIPackagesDoNotImportCardigann(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go tool not available")
	}

	const forbidden = "github.com/buzzqw/indexarr/internal/cardigann"
	packages := []string{
		"github.com/buzzqw/indexarr/internal/web",
		"github.com/buzzqw/indexarr/internal/adminapi",
	}
	for _, pkg := range packages {
		t.Run(pkg, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()

			cmd := exec.CommandContext(ctx, "go", "list", "-deps", pkg)
			output, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("go list -deps %s: %v\n%s", pkg, err, output)
			}
			found := make(map[string]bool)
			for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
				found[strings.TrimSpace(line)] = true
			}
			if found[forbidden] {
				t.Fatalf("%s depends on %s; the converter must stay CLI-only", pkg, forbidden)
			}
			if !found[pkg] {
				t.Fatalf("go list -deps %s did not report the package itself:\n%s", pkg, output)
			}
		})
	}
}
