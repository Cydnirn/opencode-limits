package tui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/cydnirn/opencode-limits/internal/budget"
)

// renderOverview draws the screen that answers "which model is about to run
// out": three window meters, then per-model remaining budget sorted so the
// tightest model leads.
func renderOverview(t Theme, s Snapshot, width int, warning, danger int) string {
	var b strings.Builder

	b.WriteString(renderHeader(t, s, width))
	b.WriteString("\n\n")

	// Three meter gauges side by side when there is room, stacked otherwise.
	b.WriteString(t.Header.Render("PLAN WINDOWS"))
	b.WriteString("\n")
	b.WriteString(renderMeters(t, s, width, warning, danger))
	b.WriteString("\n\n")

	b.WriteString(renderModelBudgetTable(t, s, width, warning, danger))
	return b.String()
}

// renderMeters draws the three window meters.
func renderMeters(t Theme, s Snapshot, width int, warning, danger int) string {
	views := s.Windows
	if len(views) == 0 {
		return t.Dim.Render("  no window data")
	}

	// One row per window: label, bar, percent, used/limit, reset countdown.
	// Fixed parts total 62 cells, so below ~80 columns the row cannot fit; drop
	// the reset column first and then the amount column rather than overflowing,
	// which would wrap and destroy the layout.
	labelW := 14
	pctW := 6
	amtW := 22
	resetW := 16
	const fixedOverhead = 4
	showAmount := width >= 62+fixedOverhead+8
	showReset := width >= 62+8+fixedOverhead

	barW := width - labelW - pctW - fixedOverhead
	if showAmount {
		barW -= amtW
	}
	if showReset {
		barW -= resetW
	}
	if barW < 8 {
		barW = 8
	}

	var b strings.Builder
	for _, v := range views {
		m := v.Meter
		pct, ok := m.Percent()
		if !ok {
			pct = 0
		}
		label := windowLabel(v.Window)
		barFrac := 0.0
		if m.LimitMicroCents > 0 {
			barFrac = float64(m.UsedMicroCents) / float64(m.LimitMicroCents)
		}
		tone := t.ToneFor(pct, warning, danger)

		amt := fmt.Sprintf("%s / %s", budget.FormatUSD(m.UsedMicroCents), budget.FormatUSD(m.LimitMicroCents))
		pctText := "--"
		if ok {
			pctText = budget.FormatPercent(pct)
		}

		reset := ""
		if m.Exhausted() {
			reset = "LIMIT REACHED"
		} else if v.Window != budget.Month {
			reset = "resets " + budget.FormatDuration(v.TimeToReset())
		} else {
			reset = "renews " + budget.FormatDuration(v.TimeToReset())
		}

		b.WriteString("  ")
		b.WriteString(padRight(label, labelW))
		b.WriteString(renderBar(t, barFrac, barW, warning, danger))
		b.WriteString(" ")
		b.WriteString(tone.Render(padLeft(pctText, pctW)))
		if showAmount {
			b.WriteString(" ")
			b.WriteString(padRight(amt, amtW))
		}
		if showReset {
			b.WriteString(t.Dim.Render(reset))
		}
		b.WriteString("\n")
	}

	// Credit line: the balance is what extra usage draws on.
	if s.Billing.BillingMode != "" {
		credit := budget.FormatUSD(s.Billing.AvailableMicroCents)
		line := fmt.Sprintf("  %s credit available", credit)
		if !s.Billing.Funded {
			line += t.Dim.Render("  (extra usage will stop at the limit)")
		}
		b.WriteString(t.Dim.Render(line))
		b.WriteString("\n")
	}
	return clipLines(strings.TrimRight(b.String(), "\n"), width)
}

// windowLabel returns the gauge label for a window.
func windowLabel(w budget.Window) string {
	switch w {
	case budget.FiveHour:
		return "5-hour window"
	case budget.Week:
		return "Weekly"
	default:
		return "Monthly"
	}
}

// renderModelBudgetTable draws per-model spend against allocation. This is the
// view the Console is missing.
func renderModelBudgetTable(t Theme, s Snapshot, width int, warning, danger int) string {
	w := s.Window(s.FocusWindow())
	var b strings.Builder

	title := "PER-MODEL BUDGET  " + t.Accent.Render(w.Window.String()+" window")
	b.WriteString(t.Header.Render(title))
	b.WriteString("\n")

	if len(w.Models) == 0 {
		b.WriteString(t.Dim.Render("  no model usage in this window"))
		b.WriteString("\n")
		if len(s.Allocations) == 0 {
			b.WriteString(t.Dim.Render("  declare allocations in " + s.ConfigPathHint()))
			b.WriteString("\n")
		}
		return b.String()
	}

	// Column layout: model, bar, spent, allocated, remaining, %plan, ETA.
	// etaW is dropped on narrow terminals, so the mutable widths must be
	// variables; only the genuinely fixed constants stay const.
	const (
		modelMin   = 18
		barMin     = 10
		numW       = 9
		pctW       = 6
		etaDefault = 14
		gap        = 2
	)
	// Hard floors used only when the terminal is too narrow to honour
	// modelMin/barMin; going below them still beats overflowing the screen,
	// because App.View only clips vertically.
	const (
		modelFloor = 8
		barFloor   = 4
	)

	etaW := etaDefault
	numSpentW, numAllocW, numRemW := numW, numW, numW
	showPct := true

	// fixed is the width consumed by everything except the model name and bar.
	fixed := numSpentW + numAllocW + numRemW + pctW + etaW + gap*6
	avail := width - 2 - fixed

	// Drop the ETA column first: a missing ETA costs one figure, whereas a
	// squeezed model name costs the row its identity.
	if avail < modelMin+barMin {
		etaW = 0
		fixed = numSpentW + numAllocW + numRemW + pctW + gap*5
		avail = width - 2 - fixed
	}
	// Then the plan-share column, which the Models screen also carries.
	if avail < modelMin+barMin {
		showPct = false
		fixed = numSpentW + numAllocW + numRemW + gap*4
		avail = width - 2 - fixed
	}
	// Last, shrink the money columns down to their floor: "$0.00" needs six
	// cells, so below that the numbers would be unreadable anyway.
	const numFloor = 6
	if avail < modelFloor+barFloor {
		numSpentW, numAllocW, numRemW = numFloor, numFloor, numFloor
		fixed = numSpentW + numAllocW + numRemW + gap*4
		avail = width - 2 - fixed
	}

	modelW, barW := modelMin, barMin
	if avail >= modelMin+barMin {
		modelW = avail * 55 / 100
		// Clamp the name before deriving the bar, so a name pushed up to its
		// minimum cannot make the row wider than the space available.
		if modelW < modelMin {
			modelW = modelMin
		}
		barW = avail - modelW
		if barW < barMin {
			barW = barMin
			modelW = avail - barW
		}
	} else {
		// Still no room: shrink both, splitting what is left 60/40 and
		// respecting hard floors so neither column vanishes.
		spare := avail
		if spare < modelFloor+barFloor {
			spare = modelFloor + barFloor
		}
		modelW = spare * 60 / 100
		if modelW < modelFloor {
			modelW = modelFloor
		}
		barW = spare - modelW
		if barW < barFloor {
			barW = barFloor
		}
	}

	// Header row.
	hdr := []string{
		padRight("MODEL", modelW),
		padRight("SPENT vs ALLOCATED", barW),
		padLeft("SPENT", numSpentW),
		padLeft("ALLOC", numAllocW),
		padLeft("LEFT", numRemW),
	}
	if showPct {
		hdr = append(hdr, padLeft("%PLAN", pctW))
	}
	if etaW > 0 {
		hdr = append(hdr, padRight("RUNS OUT", etaW))
	}
	b.WriteString(t.Dim.Render("  " + strings.Join(hdr, strings.Repeat(" ", gap))))
	b.WriteString("\n")

	for _, m := range w.Models {
		name := s.ModelName(m.Model)
		barFrac := 0.0
		if m.AllocatedWindow > 0 {
			barFrac = float64(m.Spend()) / float64(m.AllocatedWindow)
		}
		allocText := "--"
		leftText := "--"
		leftStyle := t.Dim
		if m.HasAllocation {
			allocText = budget.FormatUSD(m.AllocatedWindow)
			leftText = budget.FormatUSD(m.Remaining)
			switch {
			case m.Exhausted:
				leftStyle = t.Bad
			case m.PercentOfAllocation >= float64(warning):
				leftStyle = t.Warn
			default:
				leftStyle = t.Good
			}
		}

		etaText := ""
		if etaW > 0 {
			switch {
			case !m.HasAllocation:
				etaText = ""
			case m.Exhausted:
				etaText = t.Bad.Render("exhausted")
			case m.ETAExceedsWindow:
				etaText = t.Dim.Render("resets first")
			case m.ETA > 0:
				// The tilde is the marker the legend below refers to: this number
				// is extrapolated from burn rate, not reported by the API.
				etaText = t.Warn.Render("~" + budget.FormatDuration(m.ETA))
			}
		}

		row := []string{
			padRight(name, modelW),
			renderBar(t, barFrac, barW, warning, danger),
			padLeft(budget.FormatUSD(m.Spend()), numSpentW),
			padLeft(allocText, numAllocW),
			leftStyle.Render(padLeft(leftText, numRemW)),
		}
		if showPct {
			row = append(row, padLeft(budget.FormatPercent(m.PercentOfPlan), pctW))
		}
		if etaW > 0 {
			row = append(row, padRight(etaText, etaW))
		}
		b.WriteString("  " + strings.Join(row, strings.Repeat(" ", gap)))
		b.WriteString("\n")
	}

	// Reconciliation row: the meter minus what the models account for. Shown,
	// never hidden, so the numbers visibly add up.
	b.WriteString("\n")
	b.WriteString(t.Dim.Render("  " + strings.Repeat("─", min(width-4, 72))))
	b.WriteString("\n")

	unattr := w.Unattributed
	unattrText := budget.FormatUSD(unattr)
	note := ""
	if unattr > 0 {
		note = fmt.Sprintf("not attributed to a model (%.0f%% of window spend)", w.UnattributedPercent)
	} else if unattr < 0 {
		note = "per-model rollups ahead of the meter; meter is authoritative"
	} else {
		note = "fully attributed"
	}
	b.WriteString("  ")
	b.WriteString(padRight("unattributed", modelW))
	// The note is the only elastic cell: whatever the numeric columns left over
	// is all it gets, and it truncates rather than pushing the row past width.
	noteW := width - 2 - modelW - numAllocW - gap*2
	if noteW < 4 {
		noteW = 4
	}
	b.WriteString(t.Dim.Render(padRight(truncateDisplay(note, noteW), noteW)))
	b.WriteString(t.Dim.Render(padLeft(unattrText, numAllocW)))
	b.WriteString("\n")

	// Allocated vs plan limit, so over-allocation is obvious. Each half is
	// written separately so the warning keeps its bad tone while still
	// wrapping inside the terminal.
	//
	// Per-model caps are independent ceilings, not shares of one pool, so summing
	// them and comparing to the plan meter would be meaningless. State the plan
	// meter, and where the caps came from, instead.
	monthly := s.Window(budget.Month)
	if monthly.HasPlanCaps {
		writeWrapped(&b, fmt.Sprintf("per-model caps from the %s plan (%s); plan meter %s of %s per month",
			s.PlanName, s.AllowanceSource, budget.FormatUSD(monthly.Meter.UsedMicroCents),
			budget.FormatUSD(monthly.Meter.LimitMicroCents)), "  ", width)
	} else if monthly.AllocatedTotal > 0 {
		writeWrapped(&b, fmt.Sprintf("%s allocated of %s monthly plan budget",
			budget.FormatUSD(monthly.AllocatedTotal), budget.FormatUSD(monthly.Meter.LimitMicroCents)), "  ", width)
	}
	if monthly.OverAllocated > 0 {
		writeWrapped(&b, fmt.Sprintf("over-allocated by %s", budget.FormatUSD(monthly.OverAllocated)), "  ", width)
	}
	// Anything marked with ~ is a projection from burn rate, not a measured
	// value reported by the API. Say so rather than letting the two blur.
	if hasProjection(w.Models) {
		writeWrapped(&b, "~ projected from burn rate, not reported by the API", "  ", width)
	}
	b.WriteString("\n")
	return clipLines(b.String(), width)
}

// clipLines hard-truncates every line to width display cells. It is the last
// line of defence for pathological terminals (say 20 columns) where no column
// arrangement can honour the requested figures; a clipped row is ugly but
// readable, whereas an over-wide line wraps and destroys the whole layout.
func clipLines(s string, width int) string {
	if width <= 0 {
		return s
	}
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		if lipgloss.Width(line) > width {
			lines[i] = truncateDisplay(line, width)
		}
	}
	return strings.Join(lines, "\n")
}

// renderHeader draws the identity and freshness line.
func renderHeader(t Theme, s Snapshot, width int) string {
	var b strings.Builder
	left := t.Title.Render(s.PlanName)
	if s.OrgName != "" {
		left += t.Dim.Render("  " + s.OrgName)
	}
	if s.Email != "" {
		left += t.Dim.Render("  " + s.Email)
	}
	b.WriteString(left)
	b.WriteString("\n")

	right := t.Dim.Render("updated " + s.Age())
	b.WriteString(right)
	if s.Err != nil {
		b.WriteString("  " + t.Warn.Render("partial: "+firstLine(s.Err.Error())))
	}
	return b.String()
}

// FocusWindow is the default window for tables: the 5-hour window is the one
// that actually stops you mid-session, so it leads.
func (s Snapshot) FocusWindow() budget.Window { return budget.FiveHour }

// ConfigPathHint returns a path string for messages.
func (s Snapshot) ConfigPathHint() string {
	if s.configPath != "" {
		return s.configPath
	}
	return "config.toml"
}

// Age renders how long ago the snapshot was fetched.
func (s Snapshot) Age() string {
	if s.FetchedAt.IsZero() {
		return "never"
	}
	d := s.Now.Sub(s.FetchedAt)
	if d < 0 {
		d = 0
	}
	if d < time.Minute {
		return "<1m ago"
	}
	return budget.FormatDuration(d) + " ago"
}

// sortedModelIDs returns model IDs ordered by name, for stable iteration.
func sortedModelIDs(s Snapshot) []string {
	ids := make([]string, 0, len(s.Catalog))
	for id := range s.Catalog {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		return s.ModelName(ids[i]) < s.ModelName(ids[j])
	})
	return ids
}

// firstLine returns the first line of a multi-line error, for headers.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// hasProjection reports whether any row carries a burn-rate projection, so the
// legend is only printed when it applies.
func hasProjection(models []budget.Attribution) bool {
	for _, m := range models {
		if m.ETA > 0 {
			return true
		}
	}
	return false
}
