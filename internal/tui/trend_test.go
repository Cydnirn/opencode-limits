package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/cydnirn/opencode-limits/internal/console"
)

// TestRenderTrendEmptyActivity is requirement (g): no activity data must not
// panic and must say so plainly.
func TestRenderTrendEmptyActivity(t *testing.T) {
	tests := []struct {
		name  string
		width int
	}{
		{name: "narrow", width: 80},
		{name: "wide", width: 120},
	}

	snap := newFixture(baseAllocations())
	snap.Activity = console.ActivityGo{}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			out := stripANSI(renderTrend(themeForTest(), snap, tt.width, 24, 70, 95))
			if !strings.Contains(out, "DAILY SPEND") {
				t.Errorf("width %d: missing header", tt.width)
			}
			if !strings.Contains(out, "no activity data available") {
				t.Errorf("width %d: empty trend should say so:\n%s", tt.width, out)
			}
			// No misleading summary figures when there is nothing to summarise.
			if strings.Contains(out, "days |") || strings.Contains(out, "0 days") {
				t.Errorf("width %d: empty trend rendered a summary:\n%s", tt.width, out)
			}
		})
	}
}

// TestRenderTrendWithActivity is requirement (g) with data: chart, summary and
// request share all render.
func TestRenderTrendWithActivity(t *testing.T) {
	tests := []struct {
		name   string
		width  int
		height int
	}{
		{name: "short terminal", width: 80, height: 24},
		{name: "tall terminal", width: 120, height: 40},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			out := stripANSI(renderTrend(themeForTest(), newFixture(baseAllocations()), tt.width, tt.height, 70, 95))

			// Summary line: 30 daily rows totalling $30.
			if !strings.Contains(out, "30 days") {
				t.Errorf("width %d: missing day count:\n%s", tt.width, out)
			}
			if !strings.Contains(out, "total $30.00") {
				t.Errorf("width %d: missing total:\n%s", tt.width, out)
			}
			if !strings.Contains(out, "peak $1.50 on") {
				t.Errorf("width %d: missing peak day:\n%s", tt.width, out)
			}
			if !strings.Contains(out, "since 2026-10-19") {
				t.Errorf("width %d: missing activity start date:\n%s", tt.width, out)
			}
			if !strings.Contains(out, "REQUESTS BY MODEL") {
				t.Errorf("width %d: missing request section", tt.width)
			}
			if w, line := widestLine(out); w > tt.width {
				t.Errorf("width %d: line is %d columns: %q", tt.width, w, line)
			}
		})
	}
}

// TestRenderTrendRequestShareOrdering proves the request rows lead with the
// busiest model, and that percentages sum to roughly 100.
func TestRenderTrendRequestShareOrdering(t *testing.T) {
	out := stripANSI(renderTrend(themeForTest(), newFixture(baseAllocations()), 120, 40, 70, 95))

	// Haiku has 2100 requests of 4460 total, so it leads.
	haiku := lineIndexContaining(out, "Claude Haiku")
	sonnet := lineIndexContaining(out, "Claude Sonnet 4")
	gpt := lineIndexContaining(out, "GPT-5")
	gemini := lineIndexContaining(out, "Gemini 2.5 Pro")
	if !(haiku < sonnet && sonnet < gpt && gpt < gemini) {
		t.Errorf("request rows not sorted descending: haiku=%d sonnet=%d gpt=%d gemini=%d\n%s",
			haiku, sonnet, gpt, gemini, out)
	}

	line := lineWith(t, out, "Claude Haiku")
	if !strings.Contains(line, "2100") {
		t.Errorf("haiku row = %q, want its raw request count", line)
	}
	if !strings.Contains(line, "47%") {
		t.Errorf("haiku row = %q, want 47%% share of requests", line)
	}
}

// TestRenderTrendChartHasDateLabels proves the axis survived the ntcharts
// option fix: legible date labels are the whole point of the chart.
func TestRenderTrendChartHasDateLabels(t *testing.T) {
	tests := []struct {
		name  string
		width int
	}{
		{name: "narrow", width: 80},
		{name: "wide", width: 120},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			out := stripANSI(renderTrend(themeForTest(), newFixture(baseAllocations()), tt.width, 40, 70, 95))

			// shortDate renders MM-DD; the newest fixture day is 11-18.
			if !strings.Contains(out, "11-18") {
				t.Errorf("width %d: chart has no legible date labels:\n%s", tt.width, out)
			}
			// A label truncated to "11-" means the bars got too narrow.
			if strings.Contains(out, "11- ") {
				t.Errorf("width %d: date labels are truncated:\n%s", tt.width, out)
			}
		})
	}
}

// TestRenderTrendSingleDay covers a one-day activity window, where the bar
// width calculation has the least room to work with.
func TestRenderTrendSingleDay(t *testing.T) {
	snap := newFixture(baseAllocations())
	snap.Activity = console.ActivityGo{
		StartsAt: snap.Now.Add(-24 * time.Hour),
		Daily: []console.ActivityDay{
			{Date: "2026-11-18", Cost: console.FromUSD(1.25), Tokens: 10_000, Requests: 12},
		},
		Models: []console.ActivityModel{{Provider: "anthropic", Model: modelHaiku, Requests: 12}},
	}

	out := stripANSI(renderTrend(themeForTest(), snap, 100, 30, 70, 95))
	if !strings.Contains(out, "1 days") {
		t.Errorf("single-day summary missing:\n%s", out)
	}
	if !strings.Contains(out, "11-18") {
		t.Errorf("single-day chart has no date label:\n%s", out)
	}
}

// TestRenderTrendZeroCostDay covers a day with no spend, which must still get a
// visible row rather than being dropped.
func TestRenderTrendZeroCostDay(t *testing.T) {
	snap := newFixture(baseAllocations())
	snap.Activity = console.ActivityGo{
		StartsAt: snap.Now.Add(-48 * time.Hour),
		Daily: []console.ActivityDay{
			{Date: "2026-11-17", Cost: 0},
			{Date: "2026-11-18", Cost: 0},
		},
	}

	out := stripANSI(renderTrend(themeForTest(), snap, 100, 30, 70, 95))
	if !strings.Contains(out, "total $0.00") {
		t.Errorf("zero-cost summary = wrong:\n%s", out)
	}
	if !strings.Contains(out, "no per-model request data") {
		t.Errorf("missing empty request section:\n%s", out)
	}
}

// TestRenderDailyChartBarCount covers the bar cap: bars must stay wide enough
// for a full MM-DD label.
func TestRenderDailyChartBarCount(t *testing.T) {
	tests := []struct {
		name      string
		width     int
		days      int
		wantLabel bool
	}{
		{name: "wide keeps labels", width: 120, days: 30, wantLabel: true},
		{name: "narrow keeps labels", width: 80, days: 30, wantLabel: true},
		{name: "very narrow still keeps labels", width: 40, days: 30, wantLabel: true},
		{name: "single day", width: 40, days: 1, wantLabel: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			snap := newFixture(baseAllocations())
			snap.Activity = console.ActivityGo{StartsAt: snap.Now.Add(-time.Duration(tt.days) * 24 * time.Hour)}
			for i := range tt.days {
				snap.Activity.Daily = append(snap.Activity.Daily, console.ActivityDay{
					Date: snap.Now.Add(time.Duration(i-tt.days+1) * 24 * time.Hour).Format("2006-01-02"),
					Cost: console.FromUSD(1),
				})
			}

			got := stripANSI(renderDailyChart(themeForTest(), snap, tt.width, 10))
			hasLabel := strings.Contains(got, "11-18")
			if hasLabel != tt.wantLabel {
				t.Errorf("width %d: label present = %v, want %v\n%s", tt.width, hasLabel, tt.wantLabel, got)
			}
		})
	}
}

// TestShortDate covers the MM-DD formatter, including strings too short to
// slice.
func TestShortDate(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "full date", in: "2026-11-18", want: "11-18"},
		{name: "date with time suffix", in: "2026-11-18T12:00:00Z", want: "11-18"},
		{name: "short string passes through", in: "11-18", want: "11-18"},
		{name: "empty passes through", in: "", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := shortDate(tt.in); got != tt.want {
				t.Errorf("shortDate(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// TestIndentBlock covers the chart indenter, which must leave blank lines
// unindented so trailing whitespace never inflates column width.
func TestIndentBlock(t *testing.T) {
	tests := []struct {
		name   string
		in     string
		prefix string
		want   string
	}{
		{name: "single line", in: "bar", prefix: "  ", want: "  bar"},
		{name: "blank lines untouched", in: "bar\n\nbaz", prefix: "  ", want: "  bar\n\n  baz"},
		// Splitting "" yields one empty line, which stays unindented.
		{name: "empty input", in: "", prefix: "  ", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := indentBlock(tt.in, tt.prefix); got != tt.want {
				t.Errorf("indentBlock(%q, %q) = %q, want %q", tt.in, tt.prefix, got, tt.want)
			}
		})
	}
}
