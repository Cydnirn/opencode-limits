package tui

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/cydnirn/opencode-limits/internal/budget"
	"github.com/cydnirn/opencode-limits/internal/console"
)

// TestMain forces ASCII mode for every test in this package. Rendering is the
// only thing under test here, and the ASCII glyph set keeps assertions free of
// colour escapes and of the CI machine's locale. Theme construction still goes
// through NewTheme so the real detection path is what produces it.
func TestMain(m *testing.M) {
	os.Setenv("OPENCODE_LIMITS_ASCII", "1")
	os.Exit(m.Run())
}

// Model IDs used by the fixtures: the exact strings an operator writes as
// allocation keys, so the tests exercise the real attribution path.
const (
	modelSonnet = "anthropic/claude-sonnet-4"
	modelHaiku  = "anthropic/claude-haiku"
	modelGPT    = "openai/gpt-5"
	modelGemini = "google/gemini-2.5-pro"
)

// themeForTest returns the package theme under the ASCII environment set by
// TestMain. Every render test takes it from here so nothing reads the
// developer's terminal by accident.
func themeForTest() Theme { return NewTheme() }

// fixtureNow is the frozen clock every fixture is built against. Rendering
// must never consult time.Now or these tests would drift.
func fixtureNow() time.Time { return time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC) }

// fixtureCatalog gives every model a display name distinct from its ID so
// assertions can prove the catalog lookup happened.
func fixtureCatalog() map[string]console.CatalogModel {
	return map[string]console.CatalogModel{
		modelSonnet: {
			ID:                modelSonnet,
			Name:              "Claude Sonnet 4",
			ContextLimit:      200_000,
			OutputLimit:       64_000,
			InputPerMTok:      3,
			OutputPerMTok:     15,
			CacheReadPerMTok:  0.3,
			CacheWritePerMTok: 3.75,
		},
		modelHaiku: {
			ID:               modelHaiku,
			Name:             "Claude Haiku",
			ContextLimit:     200_000,
			OutputLimit:      32_000,
			InputPerMTok:     0.8,
			OutputPerMTok:    4,
			CacheReadPerMTok: 0.08,
		},
		modelGPT: {
			ID:                modelGPT,
			Name:              "GPT-5",
			ContextLimit:      400_000,
			OutputLimit:       128_000,
			InputPerMTok:      1.25,
			OutputPerMTok:     10,
			HasTimeMultiplier: true,
			MultiplierFactor:  2,
			MultiplierDays:    []string{"mon", "tue", "wed", "thu", "fri"},
		},
		modelGemini: {
			ID:                 modelGemini,
			Name:               "Gemini 2.5 Pro",
			ContextLimit:       1_000_000,
			OutputLimit:        65_536,
			InputPerMTok:       1.25,
			OutputPerMTok:      10,
			HasContextOverride: true,
			OverrideAtContext:  200_000,
			OverrideInputRate:  2.5,
		},
	}
}

// fixtureMeters is a Go plan partway through every window. The 12/30/60 shape
// matches the published plan, so window scaling is 0.2 and 0.5.
func fixtureMeters(now time.Time) console.Meters {
	return console.Meters{
		FiveHour: console.Meter{
			StartsAt:        now.Add(-2 * time.Hour),
			ResetsAt:        now.Add(3 * time.Hour),
			LimitMicroCents: console.FromUSD(12),
			UsedMicroCents:  console.FromUSD(7.5),
		},
		Week: console.Meter{
			StartsAt:        now.Add(-2 * 24 * time.Hour),
			ResetsAt:        now.Add(5 * 24 * time.Hour),
			LimitMicroCents: console.FromUSD(30),
			UsedMicroCents:  console.FromUSD(12),
		},
		Month: console.Meter{
			// The monthly meter carries no startsAt; the period start does.
			ResetsAt:        now.Add(12 * 24 * time.Hour),
			LimitMicroCents: console.FromUSD(60),
			UsedMicroCents:  console.FromUSD(25),
		},
	}
}

// fixtureUsage is per-model spend inside each window's own period.
func fixtureUsage() map[budget.Window][]console.ModelUsage {
	row := func(model string, reqs, in, out int64, costUSD float64) console.ModelUsage {
		return console.ModelUsage{
			Model:          model,
			Provider:       strings.SplitN(model, "/", 2)[0],
			Requests:       reqs,
			InputTokens:    in,
			OutputTokens:   out,
			CacheRead:      in / 4,
			CostMicroCents: console.FromUSD(costUSD),
		}
	}
	return map[budget.Window][]console.ModelUsage{
		// 5-hour: 6.60 attributed against a 7.50 meter, leaving 0.90
		// unattributed — the reconciliation row's whole reason to exist.
		budget.FiveHour: {
			row(modelSonnet, 120, 900_000, 60_000, 5.00),
			row(modelGPT, 30, 200_000, 15_000, 0.40),
			row(modelHaiku, 80, 400_000, 20_000, 0.90),
			row(modelGemini, 12, 150_000, 8_000, 0.30),
		},
		budget.Week: {
			row(modelSonnet, 640, 4_800_000, 320_000, 18.00),
			row(modelGPT, 210, 1_400_000, 90_000, 3.20),
			row(modelHaiku, 380, 2_000_000, 110_000, 4.10),
			row(modelGemini, 60, 700_000, 40_000, 1.60),
		},
		budget.Month: {
			row(modelSonnet, 2_400, 18_000_000, 1_200_000, 62.00),
			row(modelGPT, 900, 6_000_000, 400_000, 14.00),
			row(modelHaiku, 1_500, 8_000_000, 450_000, 16.00),
			row(modelGemini, 260, 3_000_000, 180_000, 7.00),
		},
	}
}

// fixtureActivity is 30 days of daily spend plus per-model request counts,
// with Haiku the busiest model so ordering is observable.
func fixtureActivity(now time.Time) console.ActivityGo {
	act := console.ActivityGo{StartsAt: now.Add(-30 * 24 * time.Hour)}
	for i := range 30 {
		act.Daily = append(act.Daily, console.ActivityDay{
			Date:     now.Add(time.Duration(i-29) * 24 * time.Hour).Format("2006-01-02"),
			Cost:     console.FromUSD(0.50 + float64(i%5)*0.25),
			Tokens:   int64(100_000 + i*1_000),
			Requests: int64(20 + i),
		})
	}
	act.Models = []console.ActivityModel{
		{Provider: "anthropic", Model: modelSonnet, Requests: 1_400},
		{Provider: "anthropic", Model: modelHaiku, Requests: 2_100},
		{Provider: "openai", Model: modelGPT, Requests: 700},
		{Provider: "google", Model: modelGemini, Requests: 260},
	}
	return act
}

// newFixture builds a Snapshot entirely from literals. Windows come from
// budget.Build rather than a hand-written WindowView, so the tests exercise the
// real attribution code instead of a fiction that might drift from it.
func newFixture(allocs map[string]float64) Snapshot {
	now := fixtureNow()
	meters := fixtureMeters(now)
	catalog := fixtureCatalog()
	return Snapshot{
		FetchedAt: now.Add(-30 * time.Second),
		Now:       now,
		Status: console.GoStatus{
			Product: "go",
			Access: &console.GoAccess{
				StartsAt: now.Add(-20 * 24 * time.Hour),
				EndsAt:   now.Add(10 * 24 * time.Hour),
				Meters:   meters,
			},
		},
		Billing: console.BillingStatus{
			BillingMode:         "credits",
			BalanceMicroCents:   console.FromUSD(5),
			AvailableMicroCents: console.FromUSD(5),
			Funded:              true,
		},
		Catalog:     catalog,
		Enabled:     []console.EnabledModel{{ID: modelSonnet, Name: "Claude Sonnet 4", Enabled: true}},
		Windows:     budget.Build(budget.Input{Now: now, Meters: meters, Allocations: allocs, UsageByWindow: fixtureUsage(), Catalog: catalog}),
		Activity:    fixtureActivity(now),
		Allocations: allocs,
		PlanName:    "OpenCode Go",
		OrgName:     "Acme",
		Email:       "dev@example.com",
		configPath:  "/home/dev/.config/opencode-limits/config.toml",
	}
}

// baseAllocations fits comfortably inside the monthly limit. Scaled into the
// 5-hour window (0.2) it gives Sonnet $4.00 against $5.00 spent — exhausted —
// Haiku $0.10 left, GPT-5 $1.60 left, and Gemini nothing declared.
func baseAllocations() map[string]float64 {
	return map[string]float64{
		modelSonnet: 20,
		modelGPT:    10,
		modelHaiku:  5,
	}
}

// overAllocations exceeds the monthly limit, so the plan is over-committed:
// 3 * $25 = $75 monthly, $15 in the 5-hour window against a $12 limit.
func overAllocations() map[string]float64 {
	return map[string]float64{
		modelSonnet: 25,
		modelGPT:    25,
		modelHaiku:  25,
	}
}

// strErr is a minimal error so fixtures need no fmt import, and so a wrapped
// chain can be built in one line.
type strErr string

func (e strErr) Error() string { return string(e) }

// errFromString builds an error carrying an arbitrary message.
func errFromString(s string) error { return strErr(s) }

// widestLine returns the widest display width across lines, and that line.
func widestLine(s string) (int, string) {
	worst, worstLine := 0, ""
	for _, line := range strings.Split(s, "\n") {
		if w := lipgloss.Width(line); w > worst {
			worst, worstLine = w, line
		}
	}
	return worst, worstLine
}

// strippedView renders through a screen and strips ANSI, which is how every
// content assertion reads the output.
func strippedView(t *testing.T, render func(Theme, Snapshot, int, int) string, s Snapshot, width, height int) string {
	t.Helper()
	return stripANSI(render(themeForTest(), s, width, height))
}

// lineWith returns the first ANSI-stripped line containing needle.
func lineWith(t *testing.T, out, needle string) string {
	t.Helper()
	for _, line := range strings.Split(stripANSI(out), "\n") {
		if strings.Contains(line, needle) {
			return line
		}
	}
	t.Fatalf("no line containing %q in output:\n%s", needle, stripANSI(out))
	return ""
}

// lineIndexContaining returns the index of the first line containing needle,
// or -1. Row order is asserted with this.
func lineIndexContaining(out, needle string) int {
	for i, line := range strings.Split(stripANSI(out), "\n") {
		if strings.Contains(line, needle) {
			return i
		}
	}
	return -1
}

// screen is one renderer under test, adapted to a common signature so layout
// tests can sweep every screen.
type screen struct {
	name   string
	render func(t Theme, s Snapshot, width, height int) string
}

// screens returns every screen at the thresholds production uses.
func screens() []screen {
	const warning, danger = 70, 95
	return []screen{
		{"overview", func(t Theme, s Snapshot, w, h int) string {
			return renderOverview(t, s, w, warning, danger)
		}},
		{"models", func(t Theme, s Snapshot, w, h int) string {
			return renderModels(t, s, w, sortByRemaining, warning, danger)
		}},
		{"pricing", func(t Theme, s Snapshot, w, h int) string {
			return renderPricing(t, s, w, warning, danger)
		}},
		{"trend", func(t Theme, s Snapshot, w, h int) string {
			return renderTrend(t, s, w, h, warning, danger)
		}},
	}
}

// longFixture reproduces a realistically large workspace: the live plan exposes
// 34 models, which is far more rows than fit on a normal terminal. The base
// fixture only has a handful, so it hides every scrolling bug — the Models
// screen never overflowed and the arrow keys were never exercised.
func longFixture(allocs map[string]float64) Snapshot {
	snap := newFixture(allocs)
	now := snap.Now

	names := []string{
		"GPT-6 Luna", "GPT-5.6 Luna", "DeepSeek V4.1 Flash", "DeepSeek V4 Pro",
		"GLM-5.3", "GLM-5.2", "GLM-5.3-Flash", "Kimi K3", "Kimi K3.1 Pro",
		"Kimi K2.7 Code", "Qwen3.8 Max", "Qwen3.8 Flash", "Qwen3.7 Plus",
		"Qwen4 Max", "MiniMax-M3", "MiniMax M3.1 Flash", "MiMo V2.5",
		"MiMo V2.5 Pro", "MiMo-V2.6-Pro", "MiMo-V2.6-Flash", "Grok 4.7",
		"Gemini 4 Argon", "Claude Haiku 5.5", "Claude Sonnet 4", "Hy3", "Hy4",
		"Hy4 preview", "LongCat-2.0", "Step 5 Preview", "MiniMax M3 Flash",
		"Beam Free", "Hy4 flash Free", "LongCat 2.5 Free", "Iceman Free",
	}

	catalog := map[string]console.CatalogModel{}
	usage := make([]console.ModelUsage, 0, len(names))
	bigAllocs := map[string]float64{}
	for i, name := range names {
		id := fmt.Sprintf("model-%02d", i)
		catalog[id] = console.CatalogModel{
			ID:               id,
			Name:             name,
			ContextLimit:     1_000_000,
			OutputLimit:      128_000,
			InputPerMTok:     0.1 + float64(i%7)*0.3,
			OutputPerMTok:    0.5 + float64(i%5)*1.1,
			CacheReadPerMTok: 0.01,
		}
		// Spend descends, so sorting by remaining is a stable, checkable order.
		usage = append(usage, console.ModelUsage{
			Model:          id,
			Provider:       "opencode-go",
			Requests:       int64(1000 - i*20),
			InputTokens:    int64(500_000 - i*10_000),
			OutputTokens:   int64(60_000 - i*1_000),
			CacheRead:      int64(200_000 - i*3_000),
			CacheWrite5m:   int64(5_000 + i*100),
			CostMicroCents: console.FromUSD(float64(len(names)-i) * 0.37),
		})
		// Every third model gets an allocation, so both the allocated and
		// unallocated paths render in a long list.
		if i%3 == 0 {
			bigAllocs[id] = float64(20 + i)
		}
	}

	snap.Catalog = catalog
	snap.Allocations = bigAllocs
	usageByWindow := map[budget.Window][]console.ModelUsage{}
	for _, w := range budget.All() {
		usageByWindow[w] = usage
	}
	snap.Windows = budget.Build(budget.Input{
		Now:           now,
		Meters:        snap.Status.Access.Meters,
		Allocations:   bigAllocs,
		UsageByWindow: usageByWindow,
		Catalog:       catalog,
	})
	return snap
}
