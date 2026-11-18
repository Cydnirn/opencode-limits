package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/cydnirn/opencode-limits/internal/budget"
	"github.com/cydnirn/opencode-limits/internal/console"
)

// TestRenderOverviewRendersEverySection covers the screen's required content at
// both breakpoints.
func TestRenderOverviewRendersEverySection(t *testing.T) {
	tests := []struct {
		name  string
		width int
	}{
		{name: "narrow terminal", width: 80},
		{name: "wide terminal", width: 120},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			out := stripANSI(renderOverview(themeForTest(), newFixture(baseAllocations()), tt.width, 70, 95))

			for _, want := range []string{
				"OpenCode Go", "Acme", "dev@example.com", "updated <1m ago",
				"PLAN WINDOWS",
				"5-hour window", "Weekly", "Monthly",
				"PER-MODEL BUDGET", "5-hour window",
			} {
				if !strings.Contains(out, want) {
					t.Errorf("width %d: missing %q", tt.width, want)
				}
			}
			// The meter row carries used / limit for each window.
			for _, amount := range []string{"$7.50 / $12.00", "$12.00 / $30.00", "$25.00 / $60.00"} {
				if !strings.Contains(out, amount) {
					t.Errorf("width %d: missing meter amount %q", tt.width, amount)
				}
			}
		})
	}
}

// TestRenderOverviewPartialError proves a partial failure is reported without
// dropping the data that did load.
func TestRenderOverviewPartialError(t *testing.T) {
	snap := newFixture(baseAllocations())
	snap.Err = errFromString("fetch activity: connection reset\nsecond line")

	out := stripANSI(renderOverview(themeForTest(), snap, 120, 70, 95))
	if !strings.Contains(out, "partial: fetch activity: connection reset") {
		t.Errorf("missing partial warning:\n%s", out)
	}
	if strings.Contains(out, "second line") {
		t.Errorf("header should show only the first error line:\n%s", out)
	}
	// The meters still render.
	if !strings.Contains(out, "$7.50 / $12.00") {
		t.Errorf("partial snapshot lost its meter rows:\n%s", out)
	}
}

// TestRenderOverviewEmptyState covers a snapshot with no window data at all.
func TestRenderOverviewEmptyState(t *testing.T) {
	out := stripANSI(renderOverview(themeForTest(), Snapshot{}, 100, 70, 95))

	for _, want := range []string{"no window data", "no model usage in this window", "config.toml"} {
		if !strings.Contains(out, want) {
			t.Errorf("empty overview missing %q:\n%s", want, out)
		}
	}
}

// TestRenderMetersEmpty covers the meters-only empty state.
func TestRenderMetersEmpty(t *testing.T) {
	got := stripANSI(renderMeters(themeForTest(), Snapshot{}, 100, 70, 95))
	if !strings.Contains(got, "no window data") {
		t.Errorf("renderMeters on empty snapshot = %q, want a no-data message", got)
	}
}

// TestRenderMetersCreditLine covers the billing line, which only appears when
// the Console reports a billing mode.
func TestRenderMetersCreditLine(t *testing.T) {
	tests := []struct {
		name     string
		billing  func() Snapshot
		want     string
		notThere string
	}{
		{
			name:    "funded plan shows credit without the warning",
			billing: func() Snapshot { return newFixture(baseAllocations()) },
			want:    "$5.00 credit available",
		},
		{
			name: "unfunded plan warns that usage stops at the limit",
			billing: func() Snapshot {
				s := newFixture(baseAllocations())
				s.Billing.Funded = false
				return s
			},
			want: "extra usage will stop at the limit",
		},
		{
			name: "no billing mode hides the line",
			billing: func() Snapshot {
				s := newFixture(baseAllocations())
				s.Billing = console.BillingStatus{}
				return s
			},
			notThere: "credit available",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			out := stripANSI(renderMeters(themeForTest(), tt.billing(), 120, 70, 95))
			if tt.want != "" && !strings.Contains(out, tt.want) {
				t.Errorf("meters = %q, want %q", out, tt.want)
			}
			if tt.notThere != "" && strings.Contains(out, tt.notThere) {
				t.Errorf("meters = %q, must not contain %q", out, tt.notThere)
			}
		})
	}
}

// TestRenderMetersExhaustedWindow covers the LIMIT REACHED label and its
// precedence over the reset countdown.
func TestRenderMetersExhaustedWindow(t *testing.T) {
	snap := newFixture(baseAllocations())
	meters := fixtureMeters(snap.Now)
	meters.FiveHour.UsedMicroCents = meters.FiveHour.LimitMicroCents
	snap.Status.Access.Meters = meters
	snap.Windows = budget.Build(budget.Input{
		Now:           snap.Now,
		Meters:        meters,
		Allocations:   snap.Allocations,
		UsageByWindow: fixtureUsage(),
		Catalog:       snap.Catalog,
	})

	line := lineWith(t, renderMeters(themeForTest(), snap, 120, 70, 95), "5-hour window")
	if !strings.Contains(line, "LIMIT REACHED") {
		t.Errorf("exhausted meter row = %q, want LIMIT REACHED", line)
	}
	if strings.Contains(line, "resets") {
		t.Errorf("exhausted meter row = %q, want no reset countdown", line)
	}
}

// TestWindowLabel covers every window name, including the fallback.
func TestWindowLabel(t *testing.T) {
	tests := []struct {
		name string
		in   budget.Window
		want string
	}{
		{name: "five hour", in: budget.FiveHour, want: "5-hour window"},
		{name: "week", in: budget.Week, want: "Weekly"},
		{name: "month", in: budget.Month, want: "Monthly"},
		{name: "out of range falls back to monthly", in: budget.Window(99), want: "Monthly"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := windowLabel(tt.in); got != tt.want {
				t.Errorf("windowLabel(%v) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// TestRenderModelBudgetTableSortsLeastRemainingFirst is requirement (a): the
// table leads with the model closest to running out.
func TestRenderModelBudgetTableSortsLeastRemainingFirst(t *testing.T) {
	out := stripANSI(renderModelBudgetTable(themeForTest(), newFixture(baseAllocations()), 120, 70, 95))

	// Window scaling for the 5-hour window is 12/60 = 0.2, so:
	//   Sonnet  20*0.2 = $4.00 alloc vs $5.00 spent -> exhausted, $0.00 left
	//   Haiku    5*0.2 = $1.00 alloc vs $0.90 spent -> $0.10 left
	//   GPT-5   10*0.2 = $2.00 alloc vs $0.40 spent -> $1.60 left
	//   Gemini  nothing declared -> unallocated, ordered after allocated ones
	order := []string{"Claude Sonnet 4", "Claude Haiku", "GPT-5", "Gemini 2.5 Pro"}
	prev := -1
	for _, model := range order {
		idx := lineIndexContaining(out, model)
		if idx < 0 {
			t.Fatalf("row for %q missing:\n%s", model, out)
		}
		if idx <= prev {
			t.Errorf("%q is out of order: index %d after %d\n%s", model, idx, prev, out)
		}
		prev = idx
	}
}

// TestRenderModelBudgetTableSortsByRemainingNotSpend guards the subtle part of
// requirement (a): ordering is by remaining budget, not by raw spend. Sonnet
// spends the most and is exhausted, so spend and remaining agree there — this
// case makes them disagree.
func TestRenderModelBudgetTableSortsByRemainingNotSpend(t *testing.T) {
	// Sonnet keeps a large allocation and spends little; Haiku keeps a tiny
	// allocation and exhausts it. By spend Sonnet leads; by remaining Haiku
	// must lead.
	allocs := map[string]float64{modelSonnet: 50, modelHaiku: 0.2}
	out := stripANSI(renderModelBudgetTable(themeForTest(), newFixture(allocs), 120, 70, 95))

	haiku := lineIndexContaining(out, "Claude Haiku")
	sonnet := lineIndexContaining(out, "Claude Sonnet 4")
	if haiku < 0 || sonnet < 0 {
		t.Fatalf("rows missing:\n%s", out)
	}
	if haiku > sonnet {
		t.Errorf("Haiku (exhausted, $0 left) sorted below Sonnet (plenty left): haiku=%d sonnet=%d\n%s",
			haiku, sonnet, out)
	}
}

// TestRenderModelBudgetTableExhaustedModel is requirement (b).
func TestRenderModelBudgetTableExhaustedModel(t *testing.T) {
	out := renderModelBudgetTable(themeForTest(), newFixture(baseAllocations()), 120, 70, 95)

	line := lineWith(t, out, "Claude Sonnet 4")
	if !strings.Contains(line, "exhausted") {
		t.Errorf("exhausted row = %q, want it labelled exhausted", line)
	}
	if !strings.Contains(line, "$0.00") {
		t.Errorf("exhausted row = %q, want $0.00 remaining", line)
	}
	if strings.Contains(line, "-$") {
		t.Errorf("exhausted row = %q, want remaining floored at zero", line)
	}
	// Overspend stays truthful: the row still shows what was actually spent.
	if !strings.Contains(line, "$5.00") {
		t.Errorf("exhausted row = %q, want the real $5.00 spend", line)
	}
	// A model that is merely close warning-tones but is not called exhausted.
	haiku := lineWith(t, out, "Claude Haiku")
	if strings.Contains(haiku, "exhausted") {
		t.Errorf("non-exhausted row = %q, must not say exhausted", haiku)
	}
}

// TestRenderModelBudgetTableSpendBeyondAllocation drives spend past allocation
// by a wide margin, which is the case where a naive subtraction would render a
// large negative remaining.
func TestRenderModelBudgetTableSpendBeyondAllocation(t *testing.T) {
	allocs := map[string]float64{modelSonnet: 1} // $0.20 in the 5-hour window
	out := renderModelBudgetTable(themeForTest(), newFixture(allocs), 120, 70, 95)

	line := lineWith(t, out, "Claude Sonnet 4")
	if !strings.Contains(line, "$0.00") || strings.Contains(line, "-$") {
		t.Errorf("overspent row = %q, want $0.00 and no negative", line)
	}
	// The exhausted label is the honest signal, since %PLAN is spend against
	// the plan limit (42%) rather than against the $0.20 allocation.
	if !strings.Contains(line, "exhausted") {
		t.Errorf("overspent row = %q, want it labelled exhausted", line)
	}
}

// TestRenderModelBudgetTableUnattributed is requirement (c): the reconciliation
// row shows the meter-minus-models gap.
func TestRenderModelBudgetTableUnattributed(t *testing.T) {
	snap := newFixture(baseAllocations())
	w := snap.Window(budget.FiveHour)
	if got, want := w.Unattributed.USD(), 0.90; got < want-0.001 || got > want+0.001 {
		t.Fatalf("fixture unattributed = %.4f USD, want %.2f", got, want)
	}

	line := lineWith(t, renderModelBudgetTable(themeForTest(), snap, 120, 70, 95), "unattributed")
	if !strings.Contains(line, "$0.90") {
		t.Errorf("unattributed row = %q, want the $0.90 gap", line)
	}
	if !strings.Contains(line, "not attributed to a model") {
		t.Errorf("unattributed row = %q, want the explanatory note", line)
	}
	if !strings.Contains(line, "12%") {
		t.Errorf("unattributed row = %q, want its share of window spend", line)
	}
}

// TestRenderModelBudgetTableUnattributedEdgeCases covers the negative gap
// (rollups ahead of the meter) and the exact-match case.
func TestRenderModelBudgetTableUnattributedEdgeCases(t *testing.T) {
	tests := []struct {
		name    string
		usedUSD float64
		want    []string
	}{
		{
			name:    "rollups ahead of the meter",
			usedUSD: 5.00, // less than the 6.60 attributed
			want:    []string{"-$1.60", "meter is authoritative"},
		},
		{
			name:    "fully attributed",
			usedUSD: 6.60,
			want:    []string{"$0.00", "fully attributed"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			snap := newFixture(baseAllocations())
			meters := fixtureMeters(snap.Now)
			meters.FiveHour.UsedMicroCents = console.FromUSD(tt.usedUSD)
			snap.Status.Access.Meters = meters
			snap.Windows = budget.Build(budget.Input{
				Now:           snap.Now,
				Meters:        meters,
				Allocations:   snap.Allocations,
				UsageByWindow: fixtureUsage(),
				Catalog:       snap.Catalog,
			})

			line := lineWith(t, renderModelBudgetTable(themeForTest(), snap, 120, 70, 95), "unattributed")
			for _, want := range tt.want {
				if !strings.Contains(line, want) {
					t.Errorf("row = %q, want %q", line, want)
				}
			}
		})
	}
}

// TestRenderModelBudgetTableOverAllocated is requirement (d).
func TestRenderModelBudgetTableOverAllocated(t *testing.T) {
	tests := []struct {
		name    string
		allocs  map[string]float64
		want    string
		notWant string
	}{
		{
			name:    "fits inside the window limit",
			allocs:  baseAllocations(),
			notWant: "over-allocated",
		},
		{
			// overAllocations() declares 3 x $25 = $75 monthly against a $60
			// monthly limit, so the monthly over-allocation is $15. The summary
			// must state monthly figures because that is what its label says.
			name:   "three allocations exceed the monthly limit",
			allocs: overAllocations(),
			want:   "over-allocated by $15.00",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			out := stripANSI(renderModelBudgetTable(themeForTest(), newFixture(tt.allocs), 120, 70, 95))
			if tt.want != "" && !strings.Contains(out, tt.want) {
				t.Errorf("missing %q:\n%s", tt.want, out)
			}
			if tt.notWant != "" && strings.Contains(out, tt.notWant) {
				t.Errorf("unexpected %q:\n%s", tt.notWant, out)
			}
			// The allocation total is always stated, warning or not — and it
			// must be the monthly figure, matching the label.
			if !strings.Contains(out, "allocated of $60.00 monthly plan budget") {
				t.Errorf("missing monthly allocation summary:\n%s", out)
			}
		})
	}
}

// TestRenderModelBudgetTableNoAllocationShowsDashes is requirement (e): a model
// with no declared allocation gets "--", not a fabricated zero or a bar.
func TestRenderModelBudgetTableNoAllocationShowsDashes(t *testing.T) {
	out := renderModelBudgetTable(themeForTest(), newFixture(baseAllocations()), 120, 70, 95)
	line := lineWith(t, out, "Gemini 2.5 Pro")

	if !strings.Contains(line, "--") {
		t.Errorf("unallocated row = %q, want dashes for alloc and left", line)
	}
	if strings.Contains(line, "$0.00") {
		t.Errorf("unallocated row = %q, want no fake zero allocation", line)
	}
	if !strings.Contains(line, "$0.30") {
		t.Errorf("unallocated row = %q, want its real $0.30 spend", line)
	}
	// An unallocated model has no bar fill at all, since there is no
	// denominator to fill against.
	bar := strings.Repeat("=", 4)
	if strings.Contains(line, bar) {
		t.Errorf("unallocated row = %q, want an empty bar", line)
	}
}

// TestRenderModelBudgetTableNarrowDropsETABeforeModelName is the layout
// contract for the etaW fix: the ETA column goes before the model name, and no
// width ever overflows the terminal.
func TestRenderModelBudgetTableNarrowDropsETABeforeModelName(t *testing.T) {
	tests := []struct {
		name       string
		width      int
		wantETA    bool
		wantPct    bool
		wantHeader string
	}{
		{name: "wide keeps ETA and plan share", width: 120, wantETA: true, wantPct: true, wantHeader: "RUNS OUT"},
		{name: "100 columns keeps both", width: 100, wantETA: true, wantPct: true, wantHeader: "RUNS OUT"},
		{name: "80 columns drops ETA, keeps plan share", width: 80, wantETA: false, wantPct: true, wantHeader: "%PLAN"},
		{name: "60 columns drops ETA and plan share", width: 60, wantETA: false, wantPct: false, wantHeader: "MODEL"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			out := stripANSI(renderModelBudgetTable(themeForTest(), newFixture(baseAllocations()), tt.width, 70, 95))

			if got := strings.Contains(out, "RUNS OUT"); got != tt.wantETA {
				t.Errorf("width %d: RUNS OUT present = %v, want %v\n%s", tt.width, got, tt.wantETA, out)
			}
			if got := strings.Contains(out, "%PLAN"); got != tt.wantPct {
				t.Errorf("width %d: %%PLAN present = %v, want %v\n%s", tt.width, got, tt.wantPct, out)
			}
			if !strings.Contains(out, tt.wantHeader) {
				t.Errorf("width %d: missing header %q", tt.width, tt.wantHeader)
			}
			// Model names get squeezed last, so they always survive.
			if !strings.Contains(out, "Claude") {
				t.Errorf("width %d: model names were dropped:\n%s", tt.width, out)
			}
			if w, line := widestLine(out); w > tt.width {
				t.Errorf("width %d: line is %d columns: %q", tt.width, w, line)
			}
		})
	}
}

// TestRenderModelBudgetTableNeverOverflows sweeps the width range where the
// column arithmetic is least forgiving, since this is the regression the width
// guard exists to catch.
func TestRenderModelBudgetTableNeverOverflows(t *testing.T) {
	snap := newFixture(overAllocations())
	for width := 40; width <= 200; width++ {
		out := stripANSI(renderModelBudgetTable(themeForTest(), snap, width, 70, 95))
		if w, line := widestLine(out); w > width {
			t.Errorf("width %d: line is %d columns: %q", width, w, line)
		}
	}
}

// TestRenderModelBudgetTableEmpty covers a window with no usage at all.
func TestRenderModelBudgetTableEmpty(t *testing.T) {
	tests := []struct {
		name    string
		snap    Snapshot
		want    []string
		notWant string
	}{
		{
			name: "no usage and no allocations",
			snap: Snapshot{PlanName: "OpenCode Go"},
			want: []string{"no model usage in this window", "declare allocations in config.toml"},
		},
		{
			name:    "no usage but allocations declared",
			snap:    Snapshot{PlanName: "OpenCode Go", Allocations: map[string]float64{modelGPT: 5}},
			want:    []string{"no model usage in this window"},
			notWant: "declare allocations",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			out := stripANSI(renderModelBudgetTable(themeForTest(), tt.snap, 120, 70, 95))
			for _, want := range tt.want {
				if !strings.Contains(out, want) {
					t.Errorf("missing %q:\n%s", want, out)
				}
			}
			if tt.notWant != "" && strings.Contains(out, tt.notWant) {
				t.Errorf("unexpected %q:\n%s", tt.notWant, out)
			}
		})
	}
}

// TestRenderHeader covers the identity line's optional parts.
func TestRenderHeader(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Snapshot)
		want    []string
		notWant []string
	}{
		{
			name:   "full identity",
			mutate: func(s *Snapshot) {},
			want:   []string{"OpenCode Go", "Acme", "dev@example.com", "updated <1m ago"},
		},
		{
			name: "org and email omitted when absent",
			mutate: func(s *Snapshot) {
				s.OrgName = ""
				s.Email = ""
			},
			want:    []string{"OpenCode Go"},
			notWant: []string{"Acme", "dev@example.com"},
		},
		{
			name: "never fetched says so",
			mutate: func(s *Snapshot) {
				s.FetchedAt = time.Time{}
			},
			want: []string{"updated never"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			snap := newFixture(baseAllocations())
			tt.mutate(&snap)
			out := stripANSI(renderHeader(themeForTest(), snap, 120))
			for _, want := range tt.want {
				if !strings.Contains(out, want) {
					t.Errorf("header = %q, want %q", out, want)
				}
			}
			for _, notWant := range tt.notWant {
				if strings.Contains(out, notWant) {
					t.Errorf("header = %q, must not contain %q", out, notWant)
				}
			}
		})
	}
}

// TestFocusWindow pins the default window, since every per-model figure on the
// Overview depends on it.
func TestFocusWindow(t *testing.T) {
	if got := (Snapshot{}).FocusWindow(); got != budget.FiveHour {
		t.Errorf("FocusWindow() = %v, want %v", got, budget.FiveHour)
	}
}

// TestConfigPathHint covers the fallback used in empty states.
func TestConfigPathHint(t *testing.T) {
	tests := []struct {
		name string
		path string
		want string
	}{
		{name: "real path", path: "/home/dev/config.toml", want: "/home/dev/config.toml"},
		{name: "empty falls back", path: "", want: "config.toml"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := (Snapshot{configPath: tt.path}).ConfigPathHint(); got != tt.want {
				t.Errorf("ConfigPathHint() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestSortedModelIDs covers catalog ordering by display name.
func TestSortedModelIDs(t *testing.T) {
	got := sortedModelIDs(Snapshot{Catalog: fixtureCatalog()})
	want := []string{modelHaiku, modelSonnet, modelGPT, modelGemini}

	if len(got) != len(want) {
		t.Fatalf("sortedModelIDs returned %d ids, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("position %d = %q, want %q (full: %v)", i, got[i], want[i], got)
		}
	}
}

// TestFirstLine covers the header's single-line error rendering.
func TestFirstLine(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "single line unchanged", in: "boom", want: "boom"},
		{name: "multi line keeps the first", in: "boom\nbang", want: "boom"},
		{name: "empty stays empty", in: "", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := firstLine(tt.in); got != tt.want {
				t.Errorf("firstLine(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// The summary line says "monthly plan budget", so its figures must be monthly.
// It previously printed the focused 5-hour window's totals, reporting
// "$87.60 allocated of $12.00 monthly plan budget" against a $60 monthly limit.
func TestAllocationSummaryUsesMonthlyFigures(t *testing.T) {
	app := NewApp(Loader{}, time.Minute, 70, 95)
	app.snap = longFixture(nil)
	app.width, app.height = 120, 40
	app.screen = ScreenOverview

	out := stripANSI(app.renderBody())
	monthly := app.snap.Window(budget.Month)

	wantAlloc := budget.FormatUSD(monthly.AllocatedTotal)
	wantLimit := budget.FormatUSD(monthly.Meter.LimitMicroCents)
	var line string
	for _, ln := range strings.Split(out, "\n") {
		if strings.Contains(ln, "monthly plan budget") {
			line = ln
			break
		}
	}
	if line == "" {
		t.Fatal("no allocation summary line found")
	}
	if !strings.Contains(line, wantLimit) {
		t.Errorf("summary must show the monthly limit %s, got %q", wantLimit, strings.TrimSpace(line))
	}
	if !strings.Contains(line, wantAlloc) {
		t.Errorf("summary must show the monthly allocation total %s, got %q", wantAlloc, strings.TrimSpace(line))
	}
	// The 5-hour limit must not appear on a line that claims to be monthly.
	five := app.snap.Window(budget.FiveHour)
	if five.Meter.LimitMicroCents != monthly.Meter.LimitMicroCents {
		if strings.Contains(line, budget.FormatUSD(five.Meter.LimitMicroCents)) {
			t.Errorf("summary shows the 5-hour limit under a monthly label: %q", strings.TrimSpace(line))
		}
	}
}

// The meter block used to have a hard 72-cell minimum and overflowed anything
// narrower, wrapping destructively on the primary screen.
func TestMeterBlockFitsNarrowWidths(t *testing.T) {
	app := NewApp(Loader{}, time.Minute, 70, 95)
	app.snap = longFixture(nil)
	app.height = 40
	app.screen = ScreenOverview

	for _, w := range []int{40, 50, 60, 70, 79, 80, 100, 120} {
		app.width = w
		for i, ln := range strings.Split(app.renderBody(), "\n") {
			if got := lipgloss.Width(stripANSI(ln)); got > w {
				t.Errorf("width %d: line %d is %d columns: %q", w, i, got, stripANSI(ln))
			}
		}
	}
}

// The legend promises that projections are marked with ~, so a row that shows a
// burn-rate estimate must actually carry the mark.
func TestProjectionIsMarkedWithTilde(t *testing.T) {
	app := NewApp(Loader{}, time.Minute, 70, 95)
	// The base fixture gives Claude Haiku a bounded ETA (~13m), which is the
	// cell that must carry the mark.
	app.snap = newFixture(baseAllocations())
	app.width, app.height = 130, 40
	app.screen = ScreenOverview

	out := stripANSI(app.renderBody())
	// The row for the model with a bounded ETA must carry a tilde in its
	// countdown cell, e.g. "... 7.5%  ~13m".
	marked := false
	for _, ln := range strings.Split(out, "\n") {
		if !strings.Contains(ln, "Claude Haiku") {
			continue
		}
		if strings.Contains(ln, "~") {
			marked = true
		}
	}
	if strings.Contains(out, "projected from burn rate") && !marked {
		t.Errorf("legend claims ~ marks projections, but no ETA cell is marked:\n%s", out)
	}
}

// Pathological terminal widths must truncate, not panic.
func TestTinyWidthsDoNotPanic(t *testing.T) {
	app := NewApp(Loader{}, time.Minute, 70, 95)
	app.snap = longFixture(nil)
	app.height = 20
	for _, w := range []int{1, 2, 3, 5, 10, 19, 39} {
		app.width = w
		for _, scr := range []Screen{ScreenOverview, ScreenModels, ScreenPricing, ScreenTrend} {
			app.screen = scr
			func() {
				defer func() {
					if r := recover(); r != nil {
						t.Errorf("panic at width %d screen %v: %v", w, scr, r)
					}
				}()
				_ = app.View()
			}()
		}
	}
}
