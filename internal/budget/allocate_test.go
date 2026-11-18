package budget

import (
	"testing"
	"time"

	"github.com/cydnirn/opencode-limits/internal/console"
)

func usd(v float64) console.MicroCents { return console.FromUSD(v) }

// baseMeters is the published Go allowance: 5h $12, week $30, month $60.
func baseMeters() console.Meters {
	return console.Meters{
		FiveHour: console.Meter{LimitMicroCents: usd(12), UsedMicroCents: usd(3)},
		Week:     console.Meter{LimitMicroCents: usd(30), UsedMicroCents: usd(10)},
		Month:    console.Meter{LimitMicroCents: usd(60), UsedMicroCents: usd(10)},
	}
}

func TestMicroCentsToUSD(t *testing.T) {
	// The unit is 1e-8 USD. The Console sends "1200000000" for $12.00.
	if got := console.MicroCents(1_200_000_000).USD(); got != 12.0 {
		t.Fatalf("1200000000 microCents = %v USD, want 12", got)
	}
	if got := FormatUSD(console.MicroCents(1_200_000_000)); got != "$12.00" {
		t.Fatalf("FormatUSD = %q, want $12.00", got)
	}
	if got := FormatUSD(console.MicroCents(-500_000_000)); got != "-$5.00" {
		t.Fatalf("negative FormatUSD = %q, want -$5.00", got)
	}
}

func TestWindowScaling(t *testing.T) {
	m := baseMeters()
	cases := []struct {
		w    Window
		want float64
	}{
		{FiveHour, 0.2}, // 12/60
		{Week, 0.5},     // 30/60
		{Month, 1.0},
	}
	for _, tc := range cases {
		got := windowScale(m, tc.w)
		if diff := got - tc.want; diff > 1e-9 || diff < -1e-9 {
			t.Errorf("windowScale(%v) = %v, want %v", tc.w, got, tc.want)
		}
	}
}

func TestWindowScalingFallsBackToPlanDefaults(t *testing.T) {
	// A month limit of zero must not produce NaN/Inf: fall back to defaults.
	got := windowScale(console.Meters{}, FiveHour)
	if got != 0.2 {
		t.Fatalf("windowScale with empty meters = %v, want 0.2", got)
	}
}

func TestRemainingAndSorting(t *testing.T) {
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	in := Input{
		Now:         now,
		Meters:      baseMeters(),
		Allocations: map[string]float64{"gpt-6-luna": 15, "deepseek-v4.1-flash": 100},
		UsageByWindow: map[Window][]console.ModelUsage{
			Month: {
				{Model: "gpt-6-luna", CostMicroCents: usd(5)},
				{Model: "deepseek-v4.1-flash", CostMicroCents: usd(20)},
			},
		},
	}
	monthly := findWindow(Build(in), Month)
	if len(monthly.Models) != 2 {
		t.Fatalf("got %d models, want 2", len(monthly.Models))
	}
	// Sorted by remaining ascending: GPT (15-5=10) before DeepSeek (100-20=80).
	if monthly.Models[0].Model != "gpt-6-luna" {
		t.Fatalf("first model = %q, want gpt-6-luna (least remaining)", monthly.Models[0].Model)
	}
	if got, want := monthly.Models[0].Remaining, usd(10); got != want {
		t.Errorf("gpt remaining = %v, want %v", got, want)
	}
	if got, want := monthly.Models[1].Remaining, usd(80); got != want {
		t.Errorf("deepseek remaining = %v, want %v", got, want)
	}
}

func TestExhaustedAllocationIsFlagged(t *testing.T) {
	in := Input{
		Now:         time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC),
		Meters:      baseMeters(),
		Allocations: map[string]float64{"gpt-6-luna": 15},
		UsageByWindow: map[Window][]console.ModelUsage{
			Month: {{Model: "gpt-6-luna", CostMicroCents: usd(20)}},
		},
	}
	a := findWindow(Build(in), Month).Models[0]
	if !a.Exhausted {
		t.Fatal("expected exhausted")
	}
	if a.Remaining != 0 {
		t.Fatalf("remaining = %v, want 0 (floored)", a.Remaining)
	}
	if a.PercentOfAllocation < 100 {
		t.Fatalf("percent of allocation = %v, want >=100", a.PercentOfAllocation)
	}
}

func TestOverAllocationReported(t *testing.T) {
	in := Input{
		Now:         time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC),
		Meters:      baseMeters(),
		Allocations: map[string]float64{"a": 40, "b": 40}, // $80 vs a $60 plan
		UsageByWindow: map[Window][]console.ModelUsage{
			Month: {{Model: "a", CostMicroCents: usd(1)}},
		},
	}
	monthly := findWindow(Build(in), Month)
	if got, want := monthly.AllocatedTotal, usd(80); got != want {
		t.Errorf("allocated total = %v, want %v", got, want)
	}
	if got, want := monthly.OverAllocated, usd(20); got != want {
		t.Errorf("over-allocated = %v, want %v", got, want)
	}
}

func TestNoOverAllocationWhenWithinPlan(t *testing.T) {
	in := Input{
		Now:           time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC),
		Meters:        baseMeters(),
		Allocations:   map[string]float64{"a": 20, "b": 30},
		UsageByWindow: map[Window][]console.ModelUsage{Month: {{Model: "a", CostMicroCents: usd(1)}}},
	}
	if got := findWindow(Build(in), Month).OverAllocated; got != 0 {
		t.Fatalf("over-allocated = %v, want 0", got)
	}
}

// The core reconciliation rule: the meter is truth and the gap is surfaced.
func TestUnattributedGapIsSurfaced(t *testing.T) {
	in := Input{
		Now:    time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC),
		Meters: baseMeters(), // meter used = $10
		UsageByWindow: map[Window][]console.ModelUsage{
			Month: {
				{Model: "a", CostMicroCents: usd(4)},
				{Model: "b", CostMicroCents: usd(0.5)},
			},
		},
	}
	monthly := findWindow(Build(in), Month)
	if got, want := monthly.Attributed, usd(4.5); got != want {
		t.Errorf("attributed = %v, want %v", got, want)
	}
	if got, want := monthly.Unattributed, usd(5.5); got != want {
		t.Errorf("unattributed = %v, want %v (meter 10 - attributed 4.5)", got, want)
	}
	if monthly.Meter.UsedMicroCents != usd(10) {
		t.Errorf("meter must not be rewritten; used = %v", monthly.Meter.UsedMicroCents)
	}
}

func TestNegativeUnattributedWhenRollupsLagMeter(t *testing.T) {
	// If attribution exceeds the meter, keep the sign rather than clamping:
	// a negative gap means the meter trails, which the operator should see.
	in := Input{
		Now: time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC),
		Meters: console.Meters{
			Month: console.Meter{LimitMicroCents: usd(60), UsedMicroCents: usd(1)},
		},
		UsageByWindow: map[Window][]console.ModelUsage{
			Month: {{Model: "a", CostMicroCents: usd(3)}},
		},
	}
	if got := findWindow(Build(in), Month).Unattributed; got != usd(-2) {
		t.Fatalf("unattributed = %v, want -2", got)
	}
}

func TestAllocatedModelWithNoUsageStillListed(t *testing.T) {
	in := Input{
		Now:           time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC),
		Meters:        baseMeters(),
		Allocations:   map[string]float64{"glm-5.3": 200},
		UsageByWindow: map[Window][]console.ModelUsage{},
	}
	monthly := findWindow(Build(in), Month)
	if len(monthly.Models) != 1 {
		t.Fatalf("got %d models, want 1 (allocated but unused)", len(monthly.Models))
	}
	if monthly.Models[0].Remaining != usd(200) {
		t.Fatalf("remaining = %v, want full 200", monthly.Models[0].Remaining)
	}
}

func TestUnallocatedSortedAfterAllocated(t *testing.T) {
	in := Input{
		Now:         time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC),
		Meters:      baseMeters(),
		Allocations: map[string]float64{"allocated": 10},
		UsageByWindow: map[Window][]console.ModelUsage{
			Month: {
				{Model: "unallocated-expensive", CostMicroCents: usd(50)},
				{Model: "allocated", CostMicroCents: usd(1)},
			},
		},
	}
	monthly := findWindow(Build(in), Month)
	if monthly.Models[0].Model != "allocated" {
		t.Fatalf("first = %q, want the allocated model to lead", monthly.Models[0].Model)
	}
	if !monthly.Models[0].HasAllocation {
		t.Fatal("expected HasAllocation")
	}
}

func TestETABoundedByWindowReset(t *testing.T) {
	// Window started 1h ago, barely any budget used => ETA exceeds the reset,
	// so no countdown is shown and the flag is set instead.
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	meters := console.Meters{
		FiveHour: console.Meter{
			StartsAt:        now.Add(-1 * time.Hour),
			ResetsAt:        now.Add(4 * time.Hour),
			LimitMicroCents: usd(12),
			UsedMicroCents:  usd(1),
		},
		Month: console.Meter{LimitMicroCents: usd(60), UsedMicroCents: usd(1)},
	}
	in := Input{
		Now:           now,
		Meters:        meters,
		Allocations:   map[string]float64{"m": 60},
		UsageByWindow: map[Window][]console.ModelUsage{FiveHour: {{Model: "m", CostMicroCents: usd(0.01)}}},
	}
	a := findWindow(Build(in), FiveHour).Models[0]
	if !a.ETAExceedsWindow {
		t.Fatalf("expected ETA to exceed the window; eta=%v flag=%v", a.ETA, a.ETAExceedsWindow)
	}
}

func TestETAWithinWindow(t *testing.T) {
	// $1 spent over 1h with $11 left in the window => ~11h to dry; the window
	// resets in 40h, so the countdown stands.
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	meters := console.Meters{
		FiveHour: console.Meter{
			StartsAt:        now.Add(-1 * time.Hour),
			ResetsAt:        now.Add(40 * time.Hour),
			LimitMicroCents: usd(12),
			UsedMicroCents:  usd(1),
		},
		Month: console.Meter{LimitMicroCents: usd(12), UsedMicroCents: usd(1)},
	}
	in := Input{
		Now:           now,
		Meters:        meters,
		Allocations:   map[string]float64{"m": 12}, // 5h scale is 1.0 here
		UsageByWindow: map[Window][]console.ModelUsage{FiveHour: {{Model: "m", CostMicroCents: usd(1)}}},
	}
	a := findWindow(Build(in), FiveHour).Models[0]
	if a.ETAExceedsWindow || a.ETA == 0 {
		t.Fatalf("expected a bounded ETA, got eta=%v flag=%v", a.ETA, a.ETAExceedsWindow)
	}
	if a.ETA < 10*time.Hour || a.ETA > 12*time.Hour {
		t.Fatalf("eta = %v, want ~11h", a.ETA)
	}
}

func TestZeroSpendHasNoETA(t *testing.T) {
	in := Input{
		Now:           time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC),
		Meters:        baseMeters(),
		Allocations:   map[string]float64{"m": 10},
		UsageByWindow: map[Window][]console.ModelUsage{Month: {{Model: "m", CostMicroCents: 0}}},
	}
	a := findWindow(Build(in), Month).Models[0]
	if a.ETA != 0 || a.ETAExceedsWindow {
		t.Fatal("zero spend must not produce an ETA")
	}
}

func TestMeterPercentAndExhaustion(t *testing.T) {
	m := console.Meter{LimitMicroCents: usd(12), UsedMicroCents: usd(6)}
	if p, ok := m.Percent(); !ok || p != 50 {
		t.Fatalf("percent = %v ok=%v, want 50", p, ok)
	}
	if m.Exhausted() {
		t.Fatal("should not be exhausted")
	}
	full := console.Meter{LimitMicroCents: usd(12), UsedMicroCents: usd(12)}
	if !full.Exhausted() {
		t.Fatal("expected exhausted at exactly the limit")
	}
	if _, ok := (console.Meter{}).Percent(); ok {
		t.Fatal("zero limit must report no percentage")
	}
	if got := full.Remaining(); got != 0 {
		t.Fatalf("remaining at limit = %v", got)
	}
}

func TestFormatHelpers(t *testing.T) {
	if got := FormatDuration(3*time.Hour + 12*time.Minute); got != "3h 12m" {
		t.Errorf("FormatDuration = %q", got)
	}
	if got := FormatDuration(4*24*time.Hour + 14*time.Hour); got != "4d 14h" {
		t.Errorf("FormatDuration days = %q", got)
	}
	if got := FormatTokens(172_800_000); got != "172.8M" {
		t.Errorf("FormatTokens = %q", got)
	}
	if got := FormatPercent(55.4); got != "55%" {
		t.Errorf("FormatPercent = %q", got)
	}
	if got := FormatPercent(8.25); got != "8.3%" {
		t.Errorf("FormatPercent small = %q", got)
	}
	if got := FormatDuration(0); got != "now" {
		t.Errorf("FormatDuration zero = %q", got)
	}
	if got := FormatDuration(-time.Hour); got != "now" {
		t.Errorf("FormatDuration negative = %q", got)
	}
}

func TestPlanLimitsTable(t *testing.T) {
	g := PlanLimits("go")
	if g.FiveHour.LimitMicroCents.USD() != 12 || g.Month.LimitMicroCents.USD() != 60 {
		t.Errorf("go limits = %+v", g)
	}
	p := PlanLimits("go-plus")
	if p.FiveHour.LimitMicroCents.USD() != 48 || p.Month.LimitMicroCents.USD() != 240 {
		t.Errorf("go-plus limits = %+v", p)
	}
	if PlanLimits("nonsense").Month.LimitMicroCents != g.Month.LimitMicroCents {
		t.Error("unknown product must fall back to the go table")
	}
}

func findWindow(views []WindowView, w Window) WindowView {
	for _, v := range views {
		if v.Window == w {
			return v
		}
	}
	return WindowView{}
}
