package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// press applies a key and returns the updated App.
func press(t *testing.T, app App, keys ...string) App {
	t.Helper()
	var model tea.Model = app
	for _, k := range keys {
		model, _ = model.Update(keyMsg(k))
	}
	got, ok := model.(App)
	if !ok {
		t.Fatalf("Update returned %T, want App", model)
	}
	return got
}

// newScrollableApp builds an app sized small enough that the Models screen has
// more rows than fit, which is the only way scrolling can matter.
func newScrollableApp(t *testing.T) App {
	t.Helper()
	app := NewApp(Loader{}, time.Minute, 70, 95)
	app.snap = longFixture(nil)
	app.width = 120
	app.height = 12 // ~10 body lines: far fewer than the model rows
	app.screen = ScreenModels
	return app
}

// The arrow keys did nothing on every screen: handleKey had no up/down case and
// View hard-truncated with lines[:bodyH], so rows below the fold were
// unreachable no matter what was pressed.
func TestArrowKeysScrollModelsScreen(t *testing.T) {
	app := newScrollableApp(t)

	first := app.viewport()
	if app.scroll != 0 {
		t.Fatalf("initial scroll = %d, want 0", app.scroll)
	}

	app = press(t, app, "down")
	if app.scroll != 1 {
		t.Fatalf("after one down, scroll = %d, want 1", app.scroll)
	}

	after := app.viewport()
	if after == first {
		t.Fatal("body did not change after scrolling down; the view is not following the offset")
	}

	app = press(t, app, "down", "down")
	if app.scroll != 3 {
		t.Fatalf("after three downs total, scroll = %d, want 3", app.scroll)
	}

	app = press(t, app, "up")
	if app.scroll != 2 {
		t.Fatalf("after up, scroll = %d, want 2", app.scroll)
	}
}

func TestScrollClampsAtTop(t *testing.T) {
	app := newScrollableApp(t)
	app = press(t, app, "up", "up", "up")
	if app.scroll != 0 {
		t.Fatalf("scroll = %d, want 0 (cannot scroll above the top)", app.scroll)
	}
}

func TestScrollClampsAtBottom(t *testing.T) {
	app := newScrollableApp(t)
	for i := 0; i < 200; i++ {
		app = press(t, app, "down")
	}
	maxOffset := app.maxScroll()
	if app.scroll != maxOffset {
		t.Fatalf("scroll = %d, want %d (clamped to the last page)", app.scroll, maxOffset)
	}
	if maxOffset <= 0 {
		t.Fatal("fixture should overflow the viewport so scrolling is possible")
	}
}

// PgDn/Home/End are the other keys a user reaches for.
func TestScrollPageHomeEnd(t *testing.T) {
	app := newScrollableApp(t)

	app = press(t, app, "pgdown")
	if app.scroll == 0 {
		t.Fatal("pgdown did not scroll")
	}
	if app.scroll > app.maxScroll() {
		t.Fatalf("pgdown overshot: %d > %d", app.scroll, app.maxScroll())
	}

	app = press(t, app, "end")
	if app.scroll != app.maxScroll() {
		t.Fatalf("end: scroll = %d, want %d", app.scroll, app.maxScroll())
	}

	app = press(t, app, "home")
	if app.scroll != 0 {
		t.Fatalf("home: scroll = %d, want 0", app.scroll)
	}
}

// Changing screen must not strand the offset, or switching from a long screen to
// a short one renders an empty viewport.
func TestScrollResetsOnScreenChange(t *testing.T) {
	app := newScrollableApp(t)
	app = press(t, app, "down", "down", "down")
	if app.scroll == 0 {
		t.Fatal("precondition: expected a non-zero scroll")
	}
	app = press(t, app, "1")
	if app.scroll != 0 {
		t.Fatalf("scroll = %d after switching screen, want 0", app.scroll)
	}
}

// Scrolling past the end after the data shrinks (a refresh returning fewer rows)
// must not leave the viewport off the end of the content.
func TestScrollClampedAfterRefresh(t *testing.T) {
	app := newScrollableApp(t)
	app = press(t, app, "end")
	if app.scroll == 0 {
		t.Fatal("precondition: expected scrolling to reach the end")
	}
	// A refresh that yields a single model.
	shrunk := newFixture(baseAllocations())
	for i := range shrunk.Windows {
		shrunk.Windows[i].Models = shrunk.Windows[i].Models[:1]
	}
	var model tea.Model = app
	model, _ = model.Update(refreshedMsg{snap: shrunk})
	app = model.(App)

	if app.scroll > app.maxScroll() {
		t.Fatalf("scroll = %d exceeds maxScroll %d after refresh", app.scroll, app.maxScroll())
	}
}

// The viewport must never render more lines than the terminal has room for,
// whatever the scroll offset.
func TestViewNeverExceedsHeight(t *testing.T) {
	for _, h := range []int{8, 12, 20, 40} {
		app := newScrollableApp(t)
		app.height = h
		for _, offset := range []int{0, 1, 5, 100} {
			app.scroll = offset
			v := app.View()
			lines := strings.Split(v, "\n")
			// One trailing newline yields a final empty element.
			if n := len(lines); n > 0 && lines[n-1] == "" {
				lines = lines[:n-1]
			}
			if len(lines) > h {
				t.Errorf("height %d scroll %d: rendered %d lines, limit %d",
					h, offset, len(lines), h)
			}
		}
	}
}

// A scroll indicator appears only when the content actually overflows.
func TestScrollIndicator(t *testing.T) {
	app := newScrollableApp(t)
	if !app.hasOverflow() {
		t.Fatal("fixture should overflow")
	}
	hint := stripANSI(app.renderTabs())
	// The indicator states the visible row range and the total, so the end of the
	// list is knowable instead of open-ended.
	if !strings.Contains(hint, "of ") {
		t.Errorf("overflowing screen should show a scroll range hint, got %q", hint)
	}
	if !strings.Contains(hint, fmt.Sprintf("of %d", len(app.bodyLines()))) {
		t.Errorf("hint should state the total line count, got %q", hint)
	}
	if !strings.Contains(hint, "rows 1-") {
		t.Errorf("hint should state the visible range, got %q", hint)
	}

	// Tall enough that nothing is hidden.
	roomy := newScrollableApp(t)
	roomy.height = 200
	if roomy.hasOverflow() {
		t.Fatal("tall viewport should not report overflow")
	}
}

// Growing the terminal can make the content fit, which lowers the maximum
// offset. Without re-clamping on resize the viewport stays past the end and the
// screen renders blank.
func TestResizeReclampsScroll(t *testing.T) {
	app := newScrollableApp(t)
	app = press(t, app, "end")
	if app.scroll == 0 {
		t.Fatal("precondition: expected a non-zero scroll")
	}

	var model tea.Model = app
	model, _ = model.Update(tea.WindowSizeMsg{Width: app.width, Height: 200})
	grown := model.(App)

	if grown.maxScroll() != 0 {
		t.Fatalf("tall terminal should fit everything, maxScroll = %d", grown.maxScroll())
	}
	if grown.scroll != 0 {
		t.Fatalf("scroll = %d after growing the terminal, want 0 (view would be blank)",
			grown.scroll)
	}
	if strings.TrimSpace(stripANSI(grown.viewport())) == "" {
		t.Error("viewport is blank after resize")
	}
}

// Scrolling must stop at the last page however many times the key is pressed.
// There is no unbounded scrolling: the offset is capped at maxScroll.
func TestScrollHasHardLimit(t *testing.T) {
	app := newScrollableApp(t)
	max := app.maxScroll()
	if max <= 0 {
		t.Fatal("fixture must overflow")
	}
	var model tea.Model = app
	for i := 0; i < 1000; i++ {
		model, _ = model.Update(keyMsg("down"))
	}
	got := model.(App)
	if got.scroll != max {
		t.Fatalf("after 1000 downs scroll = %d, want it pinned at maxScroll %d", got.scroll, max)
	}
	// And the last page must still show real content, not blank padding.
	view := strings.TrimSpace(stripANSI(got.viewport()))
	if view == "" {
		t.Fatal("last page is blank")
	}
	if !strings.Contains(view, "LongCat 2.5 Free") {
		t.Errorf("last page should show the final model row, got:\n%s", view)
	}
}

// The Models screen states how many models it lists.
func TestModelsScreenStatesCount(t *testing.T) {
	out := stripANSI(renderModels(themeForTest(), longFixture(nil), 140, sortByRemaining, 70, 95))
	if !strings.Contains(out, "34 models") {
		t.Errorf("models screen should state the total model count, got:\n%s",
			strings.SplitN(out, "\n", 3)[0])
	}
	if !strings.Contains(out, "allocated") || !strings.Contains(out, "unallocated") {
		t.Error("count line should split allocated from unallocated")
	}
}
