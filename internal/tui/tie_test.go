package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/cydnirn/opencode-limits/internal/budget"
	"github.com/cydnirn/opencode-limits/internal/console"
)

// tiedFixture mirrors the live workspace: most models have never been used, so
// they all compare equal on spend and requests. Without a tiebreak their order
// falls back to Go's randomised map iteration, which changes every render.
func tiedFixture() Snapshot {
	snap := newFixture(nil)
	cat := map[string]console.CatalogModel{}
	usage := make([]console.ModelUsage, 0, 24)
	for i := 0; i < 24; i++ {
		id := "zero-" + string(rune('a'+i))
		cat[id] = console.CatalogModel{ID: id, Name: id, ContextLimit: 1_000_000}
		usage = append(usage, console.ModelUsage{Model: id}) // no usage at all
	}
	snap.Catalog = cat
	ubw := map[budget.Window][]console.ModelUsage{}
	for _, w := range budget.All() {
		ubw[w] = usage
	}
	snap.Windows = budget.Build(budget.Input{
		Now: snap.Now, Meters: snap.Status.Access.Meters,
		Allocations: map[string]float64{}, UsageByWindow: ubw, Catalog: cat,
	})
	return snap
}

// The live workspace has ~22 models that have never been used, so they tie on
// every sort key. This is the case that broke scrolling: tied rows fell back to
// randomised map order, so the list reshuffled on each render and the offset
// (computed from one ordering) sliced a different one - the same models appeared
// over and over and the view never reached a stable end.
func TestTiedRowsRenderInStableOrder(t *testing.T) {
	snap := tiedFixture()
	var first string
	for i := 0; i < 12; i++ {
		out := stripANSI(renderModels(themeForTest(), snap, 140, sortByRemaining, 70, 95))
		if i == 0 {
			first = out
			continue
		}
		if out != first {
			lines := strings.Split(out, "\n")
			var rows []string
			for _, l := range lines {
				if strings.Contains(l, "zero-") {
					rows = append(rows, strings.Fields(l)[0])
				}
			}
			t.Fatalf("render %d differs from render 0; tied rows reordered.\nrow order: %v", i, rows)
		}
	}
}

// Tied rows must resolve to a fixed order: alphabetical by name, the tiebreak.
func TestTiedRowsSortByNameAsFinalTiebreak(t *testing.T) {
	snap := tiedFixture()
	out := stripANSI(renderModels(themeForTest(), snap, 140, sortByRemaining, 70, 95))
	var got []string
	for _, ln := range strings.Split(out, "\n") {
		if strings.Contains(ln, "zero-") {
			got = append(got, strings.Fields(ln)[0])
		}
	}
	if len(got) < 2 {
		t.Fatalf("expected tied rows, got %v", got)
	}
	for i := 1; i < len(got); i++ {
		if got[i-1] >= got[i] {
			t.Fatalf("tied rows not in name order at %d: %v", i, got)
		}
	}
}

// Every sort mode must be stable when all rows tie, not just the default.
func TestAllSortModesStableWithTiedRows(t *testing.T) {
	snap := tiedFixture()
	for mode := sortByRemaining; mode < sortModeCount; mode++ {
		first := stripANSI(renderModels(themeForTest(), snap, 140, mode, 70, 95))
		for i := 0; i < 5; i++ {
			again := stripANSI(renderModels(themeForTest(), snap, 140, mode, 70, 95))
			if again != first {
				t.Fatalf("sort mode %v is unstable on render %d", mode, i)
			}
		}
	}
}

// Scrolling a tied list must reach a fixed end and stay there.
func TestTiedListScrollIsBounded(t *testing.T) {
	app := NewApp(Loader{}, time.Minute, 70, 95)
	app.snap = tiedFixture()
	app.width, app.height = 140, 24
	app.screen = ScreenModels

	max := app.maxScroll()
	if max <= 0 {
		t.Fatalf("tied fixture should overflow, maxScroll = %d", max)
	}
	var model tea.Model = app
	for i := 0; i < 300; i++ {
		model, _ = model.Update(keyMsg("down"))
	}
	got := model.(App)
	if got.scroll != max {
		t.Fatalf("scroll = %d, want pinned at %d", got.scroll, max)
	}
	view := stripANSI(got.viewport())
	if strings.TrimSpace(view) == "" {
		t.Fatal("viewport blank at the end of a tied list")
	}
	if !strings.Contains(view, "zero-w") {
		t.Errorf("last page should hold the final tied rows, got:\n%s", view)
	}
}
