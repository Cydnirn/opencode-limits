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
	// scroll is the vertical offset in body lines from the top of the active
	// screen. Without it a screen taller than the terminal simply clipped and
	// the rows below the fold were unreachable.
	scroll int
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
		// Fewer rows may have come back than the current offset assumes.
		a.clampScroll()
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
		a.setScreen(ScreenOverview)
	case "2":
		a.setScreen(ScreenModels)
	case "3":
		a.setScreen(ScreenPricing)
	case "4":
		a.setScreen(ScreenTrend)
	case "tab":
		a.setScreen((a.screen + 1) % screenCount)
	case "shift+tab":
		a.setScreen((a.screen + screenCount - 1) % screenCount)

	case "down", "j":
		a.scrollBy(1)
	case "up", "k":
		a.scrollBy(-1)
	case "pgdown", " ":
		a.scrollBy(a.pageSize())
	case "pgup":
		a.scrollBy(-a.pageSize())
	case "home", "g":
		a.scroll = 0
	case "end", "G":
		a.scroll = a.maxScroll()

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

// bodyHeight is the number of lines available to screen content, after
// reserving one line each for the tab bar and the key hints.
func (a App) bodyHeight() int {
	h := a.height - 2
	if h < 1 {
		return 1
	}
	return h
}

// renderBody renders the active screen's full content, unclipped.
func (a App) renderBody() string {
	switch a.screen {
	case ScreenOverview:
		return renderOverview(a.theme, a.snap, a.width, a.warning, a.danger)
	case ScreenModels:
		return renderModels(a.theme, a.snap, a.width, a.sortMode, a.warning, a.danger)
	case ScreenPricing:
		return renderPricing(a.theme, a.snap, a.width, a.warning, a.danger)
	case ScreenTrend:
		return renderTrend(a.theme, a.snap, a.width, a.bodyHeight(), a.warning, a.danger)
	}
	return ""
}

// bodyLines splits the rendered body into lines, dropping the trailing empty
// element left by a final newline.
func (a App) bodyLines() []string {
	body := strings.TrimRight(a.renderBody(), "\n")
	if body == "" {
		return nil
	}
	return strings.Split(body, "\n")
}

// pageSize is how many lines a page-scroll moves: one viewport, less one line of
// context so the reader keeps their place across the jump.
func (a App) pageSize() int {
	n := a.bodyHeight() - 1
	if n < 1 {
		return 1
	}
	return n
}

// maxScroll is the largest useful offset: the last page, not the last line, so
// the viewport is always full of content.
func (a App) maxScroll() int {
	overflow := len(a.bodyLines()) - a.bodyHeight()
	if overflow < 0 {
		return 0
	}
	return overflow
}

// hasOverflow reports whether the active screen exceeds the viewport.
func (a App) hasOverflow() bool { return a.maxScroll() > 0 }

// clampScroll keeps the offset inside the scrollable range.
func (a *App) clampScroll() {
	max := a.maxScroll()
	if a.scroll > max {
		a.scroll = max
	}
	if a.scroll < 0 {
		a.scroll = 0
	}
}

// scrollBy moves the viewport by delta lines, clamped.
func (a *App) scrollBy(delta int) {
	a.scroll += delta
	a.clampScroll()
}

// setScreen switches screens and resets the offset, so a long screen's position
// does not strand a short one past the end of its content.
func (a *App) setScreen(s Screen) {
	a.screen = s
	a.scroll = 0
}

// viewport returns just the visible body lines, honouring the scroll offset.
// It is separated from View so tests can assert what the user actually sees
// without the tab bar and hints underneath.
func (a App) viewport() string {
	bodyH := a.bodyHeight()
	lines := a.bodyLines()

	start := a.scroll
	if start > len(lines)-bodyH {
		start = len(lines) - bodyH
	}
	if start < 0 {
		start = 0
	}
	end := start + bodyH
	if end > len(lines) {
		end = len(lines)
	}
	return strings.Join(lines[start:end], "\n")
}

// View renders the active screen.
func (a App) View() string {
	if a.quitting {
		return ""
	}
	if a.help {
		return a.renderHelp()
	}

	// clampScroll inside viewport() also covers a resize or a refresh that
	// returned fewer rows since the offset was last set.
	body := a.viewport()

	var b strings.Builder
	b.WriteString(body)
	// Pad so the tab bar stays anchored to the bottom on short screens.
	if body != "" {
		if pad := a.bodyHeight() - len(strings.Split(body, "\n")); pad > 0 {
			b.WriteString(strings.Repeat("\n", pad))
		}
	}
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
	var parts2 []string
	if a.hasOverflow() {
		// Tell the user there is more, and roughly where they are, since the
		// arrow-key affordance is otherwise invisible.
		parts2 = append(parts2, a.theme.Dim.Render(fmt.Sprintf("↕ %d/%d",
			a.scroll+a.bodyHeight(), len(a.bodyLines()))))
	}
	if a.loading {
		parts2 = append(parts2, a.theme.Dim.Render("refreshing…"))
	} else if a.lastErr != nil {
		parts2 = append(parts2, a.theme.Warn.Render("last refresh failed"))
	}
	status := strings.Join(parts2, "  ")
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
	hints := []string{"↑↓ scroll", "q quit", "r refresh", "s sort", "? help", "tab screen"}
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
		{"↑ / ↓ (k/j)", "scroll the current screen"},
		{"pgup / pgdn", "scroll one page"},
		{"home / end", "jump to top / bottom"},
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
