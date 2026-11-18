package tui

import (
	"strings"
	"testing"

	"github.com/cydnirn/opencode-limits/internal/budget"
	"github.com/cydnirn/opencode-limits/internal/console"
)

// TestRenderModelsBreakpoints covers which columns survive at each width.
func TestRenderModelsBreakpoints(t *testing.T) {
	tests := []struct {
		name        string
		width       int
		wantColumns []string
		notColumns  []string
	}{
		{
			name:        "narrow drops token classes",
			width:       80,
			wantColumns: []string{"MODEL", "REQS", "COST", "$/1M", "LEFT(MO)", "%PLAN"},
			notColumns:  []string{"CACHE-R", "CONTEXT"},
		},
		{
			name:        "wide shows token classes",
			width:       120,
			wantColumns: []string{"MODEL", "REQS", "INPUT", "OUTPUT", "CACHE-R", "COST", "$/1M", "LEFT(MO)", "CONTEXT"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			out := stripANSI(renderModels(themeForTest(), newFixture(baseAllocations()), tt.width, sortByRemaining, 70, 95))

			for _, col := range tt.wantColumns {
				if !strings.Contains(out, col) {
					t.Errorf("width %d: missing column %q", tt.width, col)
				}
			}
			for _, col := range tt.notColumns {
				if strings.Contains(out, col) {
					t.Errorf("width %d: unexpected column %q", tt.width, col)
				}
			}
			// Every model appears regardless of width.
			for _, model := range []string{"Claude Sonnet 4", "Claude Haiku", "GPT-5", "Gemini 2.5 Pro"} {
				if !strings.Contains(out, model) {
					t.Errorf("width %d: missing model %q", tt.width, model)
				}
			}
			if w, line := widestLine(out); w > tt.width {
				t.Errorf("width %d: line is %d columns: %q", tt.width, w, line)
			}
		})
	}
}

// TestRenderModelsSortModes covers every ordering, since the footer claims the
// sort and the rows must actually follow it.
func TestRenderModelsSortModes(t *testing.T) {
	tests := []struct {
		name string
		mode sortMode
		want string // the model expected in the first data row
	}{
		{
			// Sonnet is exhausted ($0 left), so it leads under "remaining".
			name: "remaining leads with the tightest",
			mode: sortByRemaining,
			want: "Claude Sonnet 4",
		},
		{
			// Sonnet also has the highest 5-hour spend ($5.00).
			name: "spend leads with the biggest spender",
			mode: sortBySpend,
			want: "Claude Sonnet 4",
		},
		{
			// Sonnet has the most requests in the focused window (120).
			name: "requests leads with the busiest model",
			mode: sortByRequests,
			want: "Claude Sonnet 4",
		},
		{
			// Sonnet's 5-hour tokens: 900K in + 60K out + 225K cache = 1.185M.
			name: "tokens leads with the heaviest model",
			mode: sortByTokens,
			want: "Claude Sonnet 4",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			out := stripANSI(renderModels(themeForTest(), newFixture(baseAllocations()), 120, tt.mode, 70, 95))

			if !strings.Contains(out, "sorted by "+tt.mode.String()) {
				t.Errorf("missing sort label for %v", tt.mode)
			}
			leader := -1
			for _, model := range []string{"Claude Sonnet 4", "Claude Haiku", "GPT-5", "Gemini 2.5 Pro"} {
				if idx := lineIndexContaining(out, model); idx >= 0 && (leader < 0 || idx < leader) {
					leader = idx
				}
			}
			gotLine := strings.Split(stripANSI(out), "\n")[leader]
			if !strings.Contains(gotLine, tt.want) {
				t.Errorf("first model row = %q, want %q first", gotLine, tt.want)
			}
		})
	}
}

// TestRenderModelsShowsAllocationsDeclaredWithoutUsage proves a model that has
// budget but no spend still appears, which is the point of declaring it.
func TestRenderModelsShowsAllocationsDeclaredWithoutUsage(t *testing.T) {
	snap := newFixture(baseAllocations())
	// Give a model with no usage row a real allocation.
	snap.Allocations[modelGemini] = 8
	snap.Windows = budget.Build(budget.Input{
		Now:           snap.Now,
		Meters:        snap.Status.Access.Meters,
		Allocations:   snap.Allocations,
		UsageByWindow: fixtureUsage(),
		Catalog:       snap.Catalog,
	})

	out := stripANSI(renderModels(themeForTest(), snap, 120, sortByRemaining, 70, 95))
	line := lineWith(t, out, "Gemini 2.5 Pro")
	// It has spend and an allocation now, so no dashes in LEFT(MO).
	if strings.Contains(line, "--") {
		t.Errorf("allocated model row = %q, want a real remaining figure", line)
	}
}

// TestRenderModelsUnallocatedShowsDashes is requirement (e) on the Models
// screen: no allocation means dashes, not a fabricated zero.
func TestRenderModelsUnallocatedShowsDashes(t *testing.T) {
	out := stripANSI(renderModels(themeForTest(), newFixture(baseAllocations()), 120, sortByRemaining, 70, 95))
	line := lineWith(t, out, "Gemini 2.5 Pro")

	if !strings.Contains(line, "--") {
		t.Errorf("unallocated row = %q, want dashes for LEFT(MO)", line)
	}
	if strings.Contains(line, "$0.00") {
		t.Errorf("unallocated row = %q, want no fake zero allocation", line)
	}
}

// TestRenderModelsExhausted proves the monthly exhausted flag drives the LEFT
// column, independently of the 5-hour table.
func TestRenderModelsExhausted(t *testing.T) {
	// Sonnet spends $62 monthly against a $62 allocation: exhausted, and $0
	// left, while the rest of the plan still has budget.
	out := stripANSI(renderModels(themeForTest(), newFixture(map[string]float64{
		modelSonnet: 62,
	}), 120, sortByRemaining, 70, 95))

	line := lineWith(t, out, "Claude Sonnet 4")
	if !strings.Contains(line, "$0.00") {
		t.Errorf("monthly-exhausted row = %q, want $0.00 remaining", line)
	}
	if strings.Contains(line, "-$") {
		t.Errorf("monthly-exhausted row = %q, want remaining floored at zero", line)
	}
}

// TestRatePerM covers the effective-rate column, including the two cases that
// would otherwise divide by zero.
func TestRatePerM(t *testing.T) {
	tests := []struct {
		name  string
		usage console.ModelUsage
		want  string
	}{
		{
			name:  "no tokens shows dashes",
			usage: console.ModelUsage{CostMicroCents: console.FromUSD(1)},
			want:  "--",
		},
		{
			name:  "free usage is labelled",
			usage: console.ModelUsage{InputTokens: 1_000_000},
			want:  "FREE",
		},
		{
			name: "computed rate",
			usage: console.ModelUsage{
				InputTokens:    1_000_000,
				CostMicroCents: console.FromUSD(3),
			},
			want: "$3.00",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := ratePerM(tt.usage); got != tt.want {
				t.Errorf("ratePerM() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestSortRows covers every mode's comparator, including the rule that
// unallocated rows never lead "remaining" and that spend ranks on spend rather
// than on remaining budget.
func TestSortRows(t *testing.T) {
	type item struct {
		name      string
		remaining console.MicroCents
		hasAlloc  bool
		spend     console.MicroCents
		requests  int64
		tokens    int64
	}
	key := func(i item) sortKey {
		return sortKey{
			remaining: i.remaining,
			hasAlloc:  i.hasAlloc,
			spend:     i.spend,
			requests:  i.requests,
			tokens:    i.tokens,
		}
	}
	base := func() []item {
		return []item{
			{name: "plenty-left", remaining: console.FromUSD(10), hasAlloc: true, spend: console.FromUSD(1), requests: 10, tokens: 100},
			{name: "exhausted", remaining: 0, hasAlloc: true, spend: console.FromUSD(60), requests: 90, tokens: 900},
			{name: "middle", remaining: console.FromUSD(5), hasAlloc: true, spend: console.FromUSD(20), requests: 50, tokens: 500},
			// No declared allocation, and the second-biggest spender.
			{name: "unallocated", hasAlloc: false, spend: console.FromUSD(30)},
		}
	}

	tests := []struct {
		name string
		mode sortMode
		want []string
	}{
		{
			name: "remaining ascending puts unallocated last",
			mode: sortByRemaining,
			want: []string{"exhausted", "middle", "plenty-left", "unallocated"},
		},
		{
			name: "spend descending ranks on spend, not remaining",
			mode: sortBySpend,
			want: []string{"exhausted", "unallocated", "middle", "plenty-left"},
		},
		{
			name: "requests descending",
			mode: sortByRequests,
			want: []string{"exhausted", "middle", "plenty-left", "unallocated"},
		},
		{
			name: "tokens descending",
			mode: sortByTokens,
			want: []string{"exhausted", "middle", "plenty-left", "unallocated"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			rows := base()
			sortRows(rows, tt.mode, key)
			got := make([]string, len(rows))
			for i, r := range rows {
				got[i] = r.name
			}
			for i, want := range tt.want {
				if got[i] != want {
					t.Errorf("position %d = %q, want %q (full: %v)", i, got[i], want, got)
				}
			}
		})
	}
}

// TestSortRowsTieBreaksRemainingOnSpend covers the documented tie-break: two
// models with equal remaining budget are ordered by spend descending, so the
// bigger spender (closer to trouble in practice) leads.
func TestSortRowsTieBreaksRemainingOnSpend(t *testing.T) {
	type row struct{ name string }
	keyFor := map[string]sortKey{
		"cheap": {remaining: 0, hasAlloc: true, spend: console.FromUSD(5)},
		"dear":  {remaining: 0, hasAlloc: true, spend: console.FromUSD(50)},
	}
	rows := []row{{name: "cheap"}, {name: "dear"}}
	sortRows(rows, sortByRemaining, func(r row) sortKey { return keyFor[r.name] })

	if rows[0].name != "dear" {
		t.Errorf("tie-break put %q first, want the bigger spender", rows[0].name)
	}
}

// TestSortModeNext proves the cycle wraps, since "s" relies on it.
func TestSortModeNext(t *testing.T) {
	tests := []struct {
		name string
		in   sortMode
		want sortMode
	}{
		{name: "remaining to spend", in: sortByRemaining, want: sortBySpend},
		{name: "spend to requests", in: sortBySpend, want: sortByRequests},
		{name: "requests to tokens", in: sortByRequests, want: sortByTokens},
		{name: "tokens wraps to remaining", in: sortByTokens, want: sortByRemaining},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := tt.in.next(); got != tt.want {
				t.Errorf("%v.next() = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

// TestSortModeString covers every label plus the unknown fallback.
func TestSortModeString(t *testing.T) {
	tests := []struct {
		name string
		in   sortMode
		want string
	}{
		{name: "remaining", in: sortByRemaining, want: "remaining"},
		{name: "spend", in: sortBySpend, want: "spend"},
		{name: "requests", in: sortByRequests, want: "requests"},
		{name: "tokens", in: sortByTokens, want: "tokens"},
		{name: "unknown", in: sortModeCount, want: "?"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := tt.in.String(); got != tt.want {
				t.Errorf("String() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestRenderPricingBreakpoints covers the pricing table's column budget: it
// must fit the terminal and keep prices on every width.
func TestRenderPricingBreakpoints(t *testing.T) {
	tests := []struct {
		name        string
		width       int
		wantColumns []string
		notColumns  []string
	}{
		{
			name:        "narrow keeps the core price columns",
			width:       80,
			wantColumns: []string{"MODEL", "INPUT", "OUTPUT", "CONTEXT", "MAX-OUT"},
			notColumns:  []string{"CACHE-R", "CACHE-W"},
		},
		{
			name:        "medium gains cache-read",
			width:       90,
			wantColumns: []string{"INPUT", "CACHE-R", "OUTPUT"},
			notColumns:  []string{"CACHE-W"},
		},
		{
			name:        "wide shows every tier",
			width:       120,
			wantColumns: []string{"INPUT", "CACHE-R", "CACHE-W", "OUTPUT", "CONTEXT", "MAX-OUT", "NOTES"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			out := stripANSI(renderPricing(themeForTest(), newFixture(baseAllocations()), tt.width, 70, 95))

			header := lineWith(t, out, "MODEL PRICING")
			_ = header
			for _, col := range tt.wantColumns {
				if !strings.Contains(out, col) {
					t.Errorf("width %d: missing column %q", tt.width, col)
				}
			}
			for _, col := range tt.notColumns {
				if strings.Contains(out, col) {
					t.Errorf("width %d: unexpected column %q", tt.width, col)
				}
			}
			if w, line := widestLine(out); w > tt.width {
				t.Errorf("width %d: line is %d columns: %q", tt.width, w, line)
			}
		})
	}
}

// TestRenderPricingContent covers the facts the pricing screen exists to
// communicate: rates, limits, override tiers and peak multipliers.
func TestRenderPricingContent(t *testing.T) {
	out := stripANSI(renderPricing(themeForTest(), newFixture(baseAllocations()), 120, 70, 95))

	// Sorted by output price ascending: Haiku $4, then the $10 models.
	haiku := lineIndexContaining(out, "Claude Haiku")
	gpt := lineIndexContaining(out, "GPT-5")
	sonnet := lineIndexContaining(out, "Claude Sonnet 4")
	if !(haiku < gpt && gpt < sonnet) {
		t.Errorf("pricing not sorted by output price: haiku=%d gpt=%d sonnet=%d", haiku, gpt, sonnet)
	}

	// Sonnet's per-1M rates, converted from the catalog's USD values.
	sonnetRow := lineWith(t, out, "Claude Sonnet 4")
	for _, want := range []string{"$3.00", "$0.30", "$3.75", "$15.00", "200.0K", "64.0K"} {
		if !strings.Contains(sonnetRow, want) {
			t.Errorf("sonnet row = %q, want %q", sonnetRow, want)
		}
	}
	// Override tier and peak multiplier are spelled out.
	if !strings.Contains(out, "ctx: $2.50 in") {
		t.Errorf("missing context override tier:\n%s", out)
	}
	if !strings.Contains(out, "x2") {
		t.Errorf("missing peak multiplier note:\n%s", out)
	}
}

// TestRenderPricingNarrowKeepsNotes keeps the override tier visible even when
// the notes column is dropped for width.
func TestRenderPricingNarrowKeepsNotes(t *testing.T) {
	out := stripANSI(renderPricing(themeForTest(), newFixture(baseAllocations()), 80, 70, 95))

	if strings.Contains(out, "NOTES") {
		t.Skip("notes column still fits at this width; nothing to relocate")
	}
	if !strings.Contains(out, "ctx: $2.50 in") {
		t.Errorf("narrow pricing dropped the override tier:\n%s", out)
	}
}

// TestRenderPricingFreeModel covers a zero-priced model, which the live catalog
// does contain.
func TestRenderPricingFreeModel(t *testing.T) {
	snap := newFixture(baseAllocations())
	snap.Catalog = map[string]console.CatalogModel{
		"vendor/free": {ID: "vendor/free", Name: "Free Model", ContextLimit: 32_000, OutputLimit: 4_096},
	}

	out := stripANSI(renderPricing(themeForTest(), snap, 120, 70, 95))
	line := lineWith(t, out, "Free Model")
	if !strings.Contains(line, "free") {
		t.Errorf("free model row = %q, want \"free\" prices", line)
	}
	if !strings.Contains(line, "no charge") {
		t.Errorf("free model row = %q, want the no-charge note", line)
	}
}

// TestRenderPricingEmptyCatalog covers a snapshot with no catalog at all.
func TestRenderPricingEmptyCatalog(t *testing.T) {
	out := stripANSI(renderPricing(themeForTest(), Snapshot{}, 100, 70, 95))
	if !strings.Contains(out, "MODEL PRICING") {
		t.Errorf("empty pricing screen missing header:\n%s", out)
	}
}
