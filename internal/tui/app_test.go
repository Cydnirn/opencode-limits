package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/cydnirn/opencode-limits/internal/budget"
	"github.com/cydnirn/opencode-limits/internal/config"
	"github.com/cydnirn/opencode-limits/internal/console"
)

// configWith builds a config carrying only allocations, which is what the
// loader reads when no file is present.
func configWith(allocs map[string]float64) config.Config {
	cfg := config.Defaults()
	cfg.Allocations = allocs
	return cfg
}

// TestMain lives in fixtures_test.go and forces ASCII mode.

// TestAppViewFitsWidth is the narrow-terminal regression guard. App.View only
// clips vertically, so any line wider than the terminal wraps and tears the
// layout; the narrow path in renderModelBudgetTable that drops the ETA column
// exists precisely to prevent that.
func TestAppViewFitsWidth(t *testing.T) {
	fixtures := map[string]Snapshot{
		"base":           newFixture(baseAllocations()),
		"over-allocated": newFixture(overAllocations()),
	}

	for _, fx := range []string{"base", "over-allocated"} {
		for _, width := range []int{80, 120} {
			for _, height := range []int{24, 40} {
				for _, sc := range screens() {
					t.Run(fx+"/"+sc.name, func(t *testing.T) {
						t.Parallel()
						// Build a fresh app per case: the subtests run
						// concurrently, so mutating a shared one would race.
						a := NewApp(Loader{}, time.Minute, 70, 95)
						a.screen = screenByName(sc.name)
						a.snap = fixtures[fx]
						a.width = width
						a.height = height

						out := a.View()
						if got := len(strings.Split(strings.TrimRight(out, "\n"), "\n")); got > height {
							t.Errorf("view is %d lines, want <= %d", got, height)
						}
						if w, line := widestLine(out); w > width {
							t.Errorf("at %dx%d: line is %d columns, want <= %d: %q",
								width, height, w, width, stripANSI(line))
						}
					})
				}
			}
		}
	}
}

// screenByName maps a screen's display name back to its enum.
func screenByName(name string) Screen {
	for s := ScreenOverview; s < screenCount; s++ {
		if s.String() == name {
			return s
		}
	}
	return ScreenOverview
}

// TestAppNavigation covers the key handling that switches screens and toggles
// help, since a broken tab cycle would strand the user on one screen.
func TestAppNavigation(t *testing.T) {
	app := NewApp(Loader{}, time.Minute, 70, 95)
	app.snap = newFixture(baseAllocations())

	tests := []struct {
		name  string
		keys  []string
		want  Screen
		help  bool
		quits bool
	}{
		{name: "starts on overview", want: ScreenOverview},
		{name: "digit 2 selects models", keys: []string{"2"}, want: ScreenModels},
		{name: "digit 3 selects pricing", keys: []string{"3"}, want: ScreenPricing},
		{name: "digit 4 selects trend", keys: []string{"4"}, want: ScreenTrend},
		{name: "tab cycles forward", keys: []string{"tab"}, want: ScreenModels},
		{name: "shift+tab cycles backward", keys: []string{"shift+tab"}, want: ScreenTrend},
		{name: "tab wraps past the last screen", keys: []string{"4", "tab"}, want: ScreenOverview},
		{name: "tab does not disturb help", keys: []string{"?"}, want: ScreenOverview, help: true},
		{name: "q quits", keys: []string{"q"}, want: ScreenOverview, quits: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			a := app
			for _, k := range tt.keys {
				next, _ := a.Update(keyMsg(k))
				a = next.(App)
			}
			if a.screen != tt.want {
				t.Errorf("screen = %v, want %v", a.screen, tt.want)
			}
			if a.help != tt.help {
				t.Errorf("help = %v, want %v", a.help, tt.help)
			}
			if a.quitting != tt.quits {
				t.Errorf("quitting = %v, want %v", a.quitting, tt.quits)
			}
		})
	}
}

// keyMsg builds the key message bubbletea would deliver for one keystroke.
func keyMsg(s string) tea.KeyMsg {
	switch s {
	case "tab":
		return tea.KeyMsg{Type: tea.KeyTab}
	case "shift+tab":
		return tea.KeyMsg{Type: tea.KeyShiftTab}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	case "right":
		return tea.KeyMsg{Type: tea.KeyRight}
	case "left":
		return tea.KeyMsg{Type: tea.KeyLeft}
	case "pgdown":
		return tea.KeyMsg{Type: tea.KeyPgDown}
	case "pgup":
		return tea.KeyMsg{Type: tea.KeyPgUp}
	case "home":
		return tea.KeyMsg{Type: tea.KeyHome}
	case "end":
		return tea.KeyMsg{Type: tea.KeyEnd}
	default:
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
	}
}

// TestAppViewClipsTallBodies proves the vertical clip works, since a long
// per-model table would otherwise push the tab bar off screen.
func TestAppViewClipsTallBodies(t *testing.T) {
	app := NewApp(Loader{}, time.Minute, 70, 95)
	app.snap = newFixture(baseAllocations())
	app.width = 100
	app.height = 12

	out := app.View()
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) > 12 {
		t.Errorf("view is %d lines, want <= 12", len(lines))
	}
	// The tab bar and hints must survive the clip.
	if !strings.Contains(out, "1 Overview") {
		t.Errorf("tab bar missing after clip:\n%s", out)
	}
	if !strings.Contains(out, "q quit") {
		t.Errorf("hints missing after clip:\n%s", out)
	}
}

// TestRenderOnceReportsEveryWindow covers the non-TUI path a cron job sees. It
// reads the snapshot directly, so it never touches the network.
func TestRenderOnceReportsEveryWindow(t *testing.T) {
	out := renderOnce(newFixture(baseAllocations()))

	for _, want := range []string{
		"opencode-limits — OpenCode Go (Acme)",
		"WINDOWS",
		"5-HOUR WINDOW — per model",
		"Weekly",
		"MONTHLY WINDOW — per model",
		"unattributed:",
		"Claude Sonnet 4",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("report missing %q:\n%s", want, out)
		}
	}
}

// TestRenderOnceAssistantRows proves the plain-text path agrees with the TUI on
// the two calibration facts: an exhausted model shows zero left, and an
// unallocated one shows dashes rather than a fabricated zero.
func TestRenderOnceAssistantRows(t *testing.T) {
	tests := []struct {
		name  string
		model string
		want  []string
		not   []string
	}{
		{
			name:  "exhausted model floors remaining at zero",
			model: "Claude Sonnet 4",
			// The report right-aligns money in a 9-wide field.
			want: []string{"$5.00", "$4.00", "$0.00", "42% of plan"},
			not:  []string{"-$"},
		},
		{
			name:  "unallocated model shows dashes",
			model: "Gemini 2.5 Pro",
			want:  []string{"alloc        --", "left        --"},
			not:   []string{"alloc     $0.00"},
		},
	}

	out := stripANSI(renderOnce(newFixture(baseAllocations())))
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			line := lineWith(t, out, tt.model)
			for _, want := range tt.want {
				if !strings.Contains(line, want) {
					t.Errorf("row = %q, want it to contain %q", line, want)
				}
			}
			for _, not := range tt.not {
				if strings.Contains(line, not) {
					t.Errorf("row = %q, must not contain %q", line, not)
				}
			}
		})
	}
}

// TestRunOnceWithoutAccess reports a failure instead of printing an empty plan.
// RunOnce surfaces the loader's own error when there is one, and invents
// "no plan data returned" only for a silently empty snapshot.
func TestRunOnceWithoutAccess(t *testing.T) {
	tests := []struct {
		name    string
		loader  Loader
		wantErr string
	}{
		{
			name:    "no client surfaces the loader error",
			loader:  Loader{},
			wantErr: "no API client",
		},
		{
			name: "config without a client still reports the loader error",
			loader: Loader{
				Config: configWith(map[string]float64{modelGPT: 5}),
			},
			wantErr: "no API client",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := RunOnce(context.Background(), tt.loader)
			if err == nil {
				t.Fatal("RunOnce returned nil error, want failure")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %q, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

// TestRenderSnapshotsDoNotPanic sweeps the ugly shapes: a zero Snapshot, no
// windows, a partial error, and allocations with no usage. A zero value must be
// renderable, which is what makes Snapshot safe to use before the first load.
func TestRenderSnapshotsDoNotPanic(t *testing.T) {
	cases := map[string]Snapshot{
		"zero value": {},
		"no windows": {
			PlanName: "OpenCode Go",
		},
		"error only": {
			PlanName: "OpenCode Go",
			Err:      errFromString("fetch usage for 5-hour window: connection reset"),
		},
		"allocations without usage": {
			PlanName:    "OpenCode Go",
			Allocations: map[string]float64{modelGPT: 5},
			Windows: budget.Build(budget.Input{
				Now:         fixtureNow(),
				Allocations: map[string]float64{modelGPT: 5},
			}),
		},
	}

	for name, snap := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			for _, sc := range screens() {
				// Recover so a panic is reported per screen rather than
				// aborting the whole run.
				func() {
					defer func() {
						if r := recover(); r != nil {
							t.Errorf("%s panicked: %v", sc.name, r)
						}
					}()
					if out := sc.render(themeForTest(), snap, 80, 24); out == "" {
						t.Errorf("%s rendered nothing", sc.name)
					}
				}()
			}
		})
	}
}

// TestZeroThemeIsUsable proves Theme's zero value renders, so a caller that
// forgets NewTheme gets plain output instead of a nil-pointer panic.
func TestZeroThemeIsUsable(t *testing.T) {
	var zero Theme
	snap := newFixture(baseAllocations())

	for _, sc := range screens() {
		t.Run(sc.name, func(t *testing.T) {
			t.Parallel()
			if out := sc.render(zero, snap, 100, 30); out == "" {
				t.Errorf("%s rendered nothing with a zero Theme", sc.name)
			}
		})
	}
}

// TestSnapshotAge covers the freshness line, including the zero and future
// cases that would otherwise render nonsense.
func TestSnapshotAge(t *testing.T) {
	now := fixtureNow()
	tests := []struct {
		name      string
		fetchedAt time.Time
		want      string
	}{
		{name: "never fetched", want: "never"},
		{name: "under a minute", fetchedAt: now.Add(-30 * time.Second), want: "<1m ago"},
		{name: "minutes", fetchedAt: now.Add(-12 * time.Minute), want: "12m ago"},
		{name: "hours", fetchedAt: now.Add(-3 * time.Hour), want: "3h 0m ago"},
		{name: "future clock skew clamps to zero", fetchedAt: now.Add(time.Hour), want: "<1m ago"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s := Snapshot{Now: now, FetchedAt: tt.fetchedAt}
			if got := s.Age(); got != tt.want {
				t.Errorf("Age() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestClosedWindowFallback covers Window() returning a usable zero view for a
// window the snapshot has no data for.
func TestClosedWindowFallback(t *testing.T) {
	s := Snapshot{}
	for _, w := range budget.All() {
		got := s.Window(w)
		if got.Window != w {
			t.Errorf("Window(%v).Window = %v, want %v", w, got.Window, w)
		}
		if len(got.Models) != 0 {
			t.Errorf("Window(%v) has %d models, want none", w, len(got.Models))
		}
	}
}

// TestModelNameFallback covers catalog, then plan list, then raw ID.
func TestModelNameFallback(t *testing.T) {
	s := Snapshot{
		Catalog: map[string]console.CatalogModel{modelGPT: {ID: modelGPT, Name: "GPT-5"}},
		Enabled: []console.EnabledModel{{ID: modelHaiku, Name: "Claude Haiku"}},
	}
	tests := []struct {
		name string
		id   string
		want string
	}{
		{name: "catalog name wins", id: modelGPT, want: "GPT-5"},
		{name: "plan list fills the gap", id: modelHaiku, want: "Claude Haiku"},
		{name: "unknown id falls back to the id", id: "vendor/unknown", want: "vendor/unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := s.ModelName(tt.id); got != tt.want {
				t.Errorf("ModelName(%q) = %q, want %q", tt.id, got, tt.want)
			}
		})
	}
}
