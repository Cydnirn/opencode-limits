package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/cydnirn/opencode-limits/internal/budget"
)

// Screen identifies the active view.
type Screen int

const (
	ScreenOverview Screen = iota
	ScreenModels
	ScreenPricing
	ScreenTrend
	screenCount
)

// String names the screen.
func (s Screen) String() string {
	switch s {
	case ScreenOverview:
		return "Overview"
	case ScreenModels:
		return "Models"
	case ScreenPricing:
		return "Pricing"
	case ScreenTrend:
		return "Trend"
	}
	return "?"
}

// refreshedMsg carries a completed load, or the error that stopped it.
type refreshedMsg struct {
	snap Snapshot
	err  error
}

// tickMsg triggers a scheduled refresh.
type tickMsg time.Time

// App is the root bubbletea model.
type App struct {
	loader Loader
	theme  Theme
	snap   Snapshot

	screen   Screen
	sortMode sortMode
	width    int
	height   int
	refresh  time.Duration
	warning  int
	danger   int
	loading  bool
	lastErr  error
	quitting bool
	help     bool
	// spinner frames the refresh indicator.
	spin int
}

// NewApp builds the root model.
func NewApp(loader Loader, refresh time.Duration, warningPercent, dangerPercent int) App {
	if refresh <= 0 {
		refresh = 60 * time.Second
	}
	return App{
		loader:  loader,
		theme:   NewTheme(),
		refresh: refresh,
		warning: warningPercent,
		danger:  dangerPercent,
		width:   100,
		height:  30,
	}
}

// Init kicks off the first load.
func (a App) Init() tea.Cmd {
	return tea.Batch(a.loadCmd(), a.tickCmd())
}

// loadCmd fetches a snapshot off the UI goroutine.
func (a App) loadCmd() tea.Cmd {
	loader := a.loader
	return func() tea.Msg {
		snap := loader.Load(context.Background())
		return refreshedMsg{snap: snap}
	}
}

// tickCmd schedules the next refresh.
func (a App) tickCmd() tea.Cmd {
	d := a.refresh
	return tea.Tick(d, func(t time.Time) tea.Msg { return tickMsg(t) })
}

// Update handles messages.
func (a App) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch m := msg.(type) {
	case tea.WindowSizeMsg:
		a.width = m.Width
		a.height = m.Height
		return a, nil

	case refreshedMsg:
		a.loading = false
		a.lastErr = m.err
		if m.err != nil && m.snap.Status.Access == nil {
			// Nothing usable came back; keep the previous snapshot but report it.
			a.snap.Err = m.err
			return a, nil
		}
		a.snap = m.snap
		if a.snap.Now.IsZero() {
			a.snap.Now = time.Now().UTC()
		}
		return a, nil

	case tickMsg:
		cmds := []tea.Cmd{a.tickCmd()}
		if !a.loading {
			a.loading = true
			cmds = append(cmds, a.loadCmd())
		}
		return a, tea.Batch(cmds...)

	case tea.KeyMsg:
		return a.handleKey(m)
	}
	return a, nil
}

// handleKey processes a keypress.
func (a App) handleKey(m tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch m.String() {
	case "q", "ctrl+c", "esc":
		if a.help {
			a.help = false
			return a, nil
		}
		a.quitting = true
		return a, tea.Quit

	case "?":
		a.help = !a.help
		return a, nil

	case "1":
		a.screen = ScreenOverview
	case "2":
		a.screen = ScreenModels
	case "3":
		a.screen = ScreenPricing
	case "4":
		a.screen = ScreenTrend
	case "tab":
		a.screen = (a.screen + 1) % screenCount
	case "shift+tab":
		a.screen = (a.screen + screenCount - 1) % screenCount

	case "s":
		a.sortMode = a.sortMode.next()

	case "r":
		if !a.loading {
			a.loading = true
			return a, a.loadCmd()
		}
	}
	return a, nil
}

// View renders the active screen.
func (a App) View() string {
	if a.quitting {
		return ""
	}
	if a.help {
		return a.renderHelp()
	}

	// Reserve two lines: tab bar and key hints.
	bodyH := a.height - 2
	var body string
	switch a.screen {
	case ScreenOverview:
		body = renderOverview(a.theme, a.snap, a.width, a.warning, a.danger)
	case ScreenModels:
		body = renderModels(a.theme, a.snap, a.width, a.sortMode, a.warning, a.danger)
	case ScreenPricing:
		body = renderPricing(a.theme, a.snap, a.width, a.warning, a.danger)
	case ScreenTrend:
		body = renderTrend(a.theme, a.snap, a.width, bodyH, a.warning, a.danger)
	}

	lines := strings.Split(strings.TrimRight(body, "\n"), "\n")
	if len(lines) > bodyH && bodyH > 0 {
		lines = lines[:bodyH]
	}

	var b strings.Builder
	b.WriteString(strings.Join(lines, "\n"))
	b.WriteString("\n")
	b.WriteString(a.renderTabs())
	b.WriteString("\n")
	b.WriteString(a.renderHints())
	return b.String()
}

// renderTabs draws the screen selector.
func (a App) renderTabs() string {
	names := []Screen{ScreenOverview, ScreenModels, ScreenPricing, ScreenTrend}
	parts := make([]string, 0, len(names))
	for i, s := range names {
		label := fmt.Sprintf("%d %s", i+1, s.String())
		if s == a.screen {
			parts = append(parts, a.theme.TabActive.Render(label))
		} else {
			parts = append(parts, a.theme.TabIdle.Render(label))
		}
	}
	left := " " + strings.Join(parts, "  ")
	status := ""
	if a.loading {
		status = a.theme.Dim.Render("refreshing…")
	} else if a.lastErr != nil {
		status = a.theme.Warn.Render("last refresh failed")
	}
	if status != "" {
		pad := a.width - visibleWidth(left) - visibleWidth(status) - 1
		if pad > 0 {
			left += strings.Repeat(" ", pad) + status
		}
	}
	return left
}

// renderHints draws the key legend.
func (a App) renderHints() string {
	hints := []string{"q quit", "r refresh", "s sort", "? help", "tab screen"}
	return a.theme.Dim.Render(" " + strings.Join(hints, "  "))
}

// renderHelp draws the help overlay.
func (a App) renderHelp() string {
	t := a.theme
	var b strings.Builder
	b.WriteString(t.Title.Render("opencode-limits"))
	b.WriteString("\n\n")
	b.WriteString("Per-model budget tracking for OpenCode Go plans.\n\n")
	rows := [][2]string{
		{"1-4 / tab", "switch screen"},
		{"s", "cycle Models sort order"},
		{"r", "refresh now"},
		{"?", "toggle this help"},
		{"q / esc", "quit"},
	}
	for _, r := range rows {
		b.WriteString("  " + t.Accent.Render(padRight(r[0], 12)) + r[1] + "\n")
	}
	b.WriteString("\n")
	b.WriteString(t.Header.Render("Screens"))
	b.WriteString("\n")
	b.WriteString("  " + t.Accent.Render(padRight("Overview", 12)) + "window meters and per-model remaining budget\n")
	b.WriteString("  " + t.Accent.Render(padRight("Models", 12)) + "tokens, cost and effective rate per model\n")
	b.WriteString("  " + t.Accent.Render(padRight("Pricing", 12)) + "per-1M prices, override tiers, peak multipliers\n")
	b.WriteString("  " + t.Accent.Render(padRight("Trend", 12)) + "daily spend and per-model request share\n")
	b.WriteString("\n")
	b.WriteString(t.Dim.Render("Allocations live in the config file; the Console has no per-model budget."))
	b.WriteString("\n")
	b.WriteString(t.Dim.Render("Press ? to return."))
	b.WriteString("\n")
	return b.String()
}

// visibleWidth is a thin wrapper so tests can reason about layout without
// importing lipgloss.
func visibleWidth(s string) int {
	return len([]rune(stripANSI(s)))
}

// stripANSI removes escape sequences for width measurement.
func stripANSI(s string) string {
	var b strings.Builder
	inEsc := false
	for _, r := range s {
		switch {
		case r == '\x1b':
			inEsc = true
		case inEsc:
			if r == 'm' {
				inEsc = false
			}
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// RunOnce performs a single load and prints a plain-text report. It is the
// non-TUI path used by cron jobs and for verification.
func RunOnce(ctx context.Context, loader Loader) (string, error) {
	snap := loader.Load(ctx)
	if snap.Status.Access == nil {
		if snap.Err != nil {
			return "", snap.Err
		}
		return "", errors.New("no plan data returned")
	}
	return renderOnce(snap), nil
}

// renderOnce formats a snapshot as plain text. It is separate from RunOnce so
// the report can be asserted from a literal without touching the network.
func renderOnce(snap Snapshot) string {
	var b strings.Builder
	fmt.Fprintf(&b, "opencode-limits — %s", snap.PlanName)
	if snap.OrgName != "" {
		fmt.Fprintf(&b, " (%s)", snap.OrgName)
	}
	b.WriteString("\n")
	fmt.Fprintf(&b, "fetched %s\n\n", snap.FetchedAt.Format(time.RFC3339))

	b.WriteString("WINDOWS\n")
	for _, w := range budget.All() {
		v := snap.Window(w)
		pct, ok := v.PercentUsed()
		pctText := "--"
		if ok {
			pctText = budget.FormatPercent(pct)
		}
		fmt.Fprintf(&b, "  %-14s %6s  %s / %s  resets %s\n",
			windowLabel(w), pctText,
			budget.FormatUSD(v.Meter.UsedMicroCents),
			budget.FormatUSD(v.Meter.LimitMicroCents),
			budget.FormatDuration(v.TimeToReset()))
	}

	for _, w := range budget.All() {
		v := snap.Window(w)
		fmt.Fprintf(&b, "\n%s WINDOW — per model\n", strings.ToUpper(w.String()))
		if len(v.Models) == 0 {
			b.WriteString("  (no usage)\n")
			continue
		}
		for _, m := range v.Models {
			alloc := "--"
			left := "--"
			if m.HasAllocation {
				alloc = budget.FormatUSD(m.AllocatedWindow)
				left = budget.FormatUSD(m.Remaining)
			}
			fmt.Fprintf(&b, "  %-30s spent %9s  alloc %9s  left %9s  %5s of plan\n",
				snap.ModelName(m.Model),
				budget.FormatUSD(m.Spend()), alloc, left,
				budget.FormatPercent(m.PercentOfPlan))
		}
		fmt.Fprintf(&b, "  unattributed: %s (%.0f%% of window spend)\n",
			budget.FormatUSD(v.Unattributed), v.UnattributedPercent)
		if v.OverAllocated > 0 {
			fmt.Fprintf(&b, "  over-allocated by %s\n", budget.FormatUSD(v.OverAllocated))
		}
	}

	if snap.Err != nil {
		fmt.Fprintf(&b, "\npartial data: %v\n", snap.Err)
	}
	return b.String()
}
