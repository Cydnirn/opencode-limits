package tui

import (
	"strings"
	"testing"

	"github.com/cydnirn/opencode-limits/internal/config"
	"github.com/cydnirn/opencode-limits/internal/console"
)

// TestLivePricingRendersRealCatalog renders the Pricing screen from the real
// Console catalog. It is skipped when no credential is present so the suite
// still runs unauthenticated.
func TestLivePricingRendersRealCatalog(t *testing.T) {
	p, _ := config.FromOSEnv().Resolve()
	c := console.NewClient(&console.Store{Path: p.CredentialFile}, p.Server)
	if err := c.Prepare(); err != nil {
		t.Skip("no credential:", err)
	}
	cat, err := c.Catalog()
	if err != nil {
		t.Fatalf("catalog must decode the live payload: %v", err)
	}
	if len(cat) == 0 {
		t.Fatal("empty catalog")
	}

	out := renderPricing(NewTheme(), Snapshot{Catalog: cat}, 120, 0, len(cat))
	if strings.TrimSpace(out) == "" {
		t.Fatal("pricing rendered empty")
	}
	// The failure this guards against surfaced as "partial: fetch catalog".
	if strings.Contains(out, "partial") || strings.Contains(out, "cannot unmarshal") {
		t.Errorf("pricing screen shows a decode error:\n%s", out)
	}
	if !strings.Contains(out, "MODEL") {
		t.Error("pricing screen is missing its header")
	}
	lines := strings.Split(out, "\n")
	t.Logf("rendered %d lines from %d models", len(lines), len(cat))
	for i, l := range lines {
		if i >= 10 {
			break
		}
		t.Logf("| %s", l)
	}
}
