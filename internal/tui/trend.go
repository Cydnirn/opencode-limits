package tui

import (
	"fmt"
	"strings"

	"github.com/NimbleMarkets/ntcharts/barchart"
	"github.com/charmbracelet/lipgloss"

	"github.com/cydnirn/opencode-limits/internal/budget"
	"github.com/cydnirn/opencode-limits/internal/console"
)

// renderTrend draws daily spend as a bar chart plus the per-model request split.
func renderTrend(t Theme, s Snapshot, width, height int, warning, danger int) string {
	var b strings.Builder

	b.WriteString(t.Header.Render("DAILY SPEND"))
	if !s.Activity.StartsAt.IsZero() {
		b.WriteString(t.Dim.Render("   since " + s.Activity.StartsAt.Format("2006-01-02")))
	}
	b.WriteString("\n\n")

	if len(s.Activity.Daily) == 0 {
		b.WriteString(t.Dim.Render("  no activity data available"))
		b.WriteString("\n")
		return b.String()
	}

	chartH := height - 14
	if chartH < 6 {
		chartH = 6
	}
	if chartH > 16 {
		chartH = 16
	}
	chartW := width - 8
	if chartW < 20 {
		chartW = 20
	}

	b.WriteString(renderDailyChart(t, s, chartW, chartH))
	b.WriteString("\n")

	// Summary figures under the chart.
	var total, peak console.MicroCents
	peakDay := ""
	for _, d := range s.Activity.Daily {
		total += d.Cost
		if d.Cost > peak {
			peak = d.Cost
			peakDay = d.Date
		}
	}
	days := len(s.Activity.Daily)
	avg := console.MicroCents(0)
	if days > 0 {
		avg = total / console.MicroCents(days)
	}

	summary := fmt.Sprintf("  %d days   total %s   avg %s/day   peak %s on %s",
		days, budget.FormatUSD(total), budget.FormatUSD(avg),
		budget.FormatUSD(peak), peakDay)
	b.WriteString(t.Subtle.Render(summary))
	b.WriteString("\n\n")

	// Per-model request share.
	b.WriteString(t.Header.Render("REQUESTS BY MODEL"))
	b.WriteString("\n")
	if len(s.Activity.Models) == 0 {
		b.WriteString(t.Dim.Render("  no per-model request data"))
		b.WriteString("\n")
		return b.String()
	}

	var reqTotal int64
	for _, m := range s.Activity.Models {
		reqTotal += m.Requests
	}
	// Sort by requests descending so the dominant model leads.
	sorted := make([]console.ActivityModel, len(s.Activity.Models))
	copy(sorted, s.Activity.Models)
	for i := 0; i < len(sorted); i++ {
		for j := i + 1; j < len(sorted); j++ {
			if sorted[j].Requests > sorted[i].Requests {
				sorted[i], sorted[j] = sorted[j], sorted[i]
			}
		}
	}

	nameW := 26
	barW := width - nameW - 24
	if barW < 8 {
		barW = 8
	}
	for _, m := range sorted {
		frac := 0.0
		if reqTotal > 0 {
			frac = float64(m.Requests) / float64(reqTotal)
		}
		line := "  " + padRight(s.ModelName(m.Model), nameW) +
			renderBar(t, frac, barW, warning, danger) + " " +
			padLeft(budget.Itoa(m.Requests), 7) +
			padLeft(budget.FormatPercent(frac*100), 7)
		b.WriteString(strings.TrimRight(line, " "))
		b.WriteString("\n")
	}
	return b.String()
}

// renderDailyChart builds an ntcharts bar chart of daily cost.
func renderDailyChart(t Theme, s Snapshot, width, height int) string {
	daily := s.Activity.Daily
	// Cap the number of bars so each bar stays wide enough to show its date
	// label. A bar narrower than "MM-DD" plus a gap truncates the label to
	// "10-", which defeats the point of the axis, so bar count follows label
	// width rather than a bare fraction of the terminal.
	const labelW = 5 // "MM-DD"
	maxBars := width / (labelW + 1)
	if maxBars < 3 {
		maxBars = 3
	}
	if len(daily) > maxBars {
		daily = daily[len(daily)-maxBars:]
	}

	// Axes stay on: the date labels under the bars are the point of this chart.
	// ntcharts calls the option WithNoAxis (singular); we simply do not use it.
	chart := barchart.New(width, height)
	chart.SetShowAxis(true)

	style := lipgloss.NewStyle().Foreground(lipgloss.Color("81"))
	if t.ASCII {
		style = lipgloss.NewStyle()
	}

	// Bars are in dollars; a value of 0 still gets a row so the day is visible.
	for _, d := range daily {
		label := shortDate(d.Date)
		chart.Push(barchart.BarData{
			Label: label,
			Values: []barchart.BarValue{{
				Name:  d.Date,
				Value: d.Cost.USD(),
				Style: style,
			}},
		})
	}
	chart.Draw()
	return indentBlock(chart.View(), "  ")
}

// shortDate renders MM-DD from a YYYY-MM-DD date.
func shortDate(s string) string {
	if len(s) >= 10 {
		return s[5:10]
	}
	return s
}

// indentBlock prefixes every line of a block.
func indentBlock(s, prefix string) string {
	lines := strings.Split(s, "\n")
	for i := range lines {
		if lines[i] != "" {
			lines[i] = prefix + lines[i]
		}
	}
	return strings.Join(lines, "\n")
}
