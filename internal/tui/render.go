package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// barWidth is the number of cells used for an inline progress bar.
const barWidth = 18

// renderBar draws a fixed-width bar with eighth-cell precision in UTF-8 mode
// and plain character cells in ASCII mode.
func renderBar(t Theme, fraction float64, width int, warning, danger int) string {
	if width <= 0 {
		return ""
	}
	if fraction < 0 {
		fraction = 0
	}
	if fraction > 1 {
		fraction = 1
	}
	g := t.Glyphs()
	fillStyle := t.BarStyle(fraction*100, warning, danger)

	if t.ASCII {
		filled := int(fraction * float64(width))
		if filled > width {
			filled = width
		}
		if filled == 0 && fraction > 0 {
			filled = 1
		}
		return fillStyle.Render(strings.Repeat(string(g.Full), filled)) +
			t.BarEmpty.Render(strings.Repeat(string(g.Empty), width-filled))
	}

	// Eighth-block resolution: total eighths across the full width.
	eighths := int(fraction*float64(width)*8 + 0.5)
	full := eighths / 8
	rem := eighths % 8
	if full > width {
		full = width
		rem = 0
	}

	var b strings.Builder
	if full > 0 {
		b.WriteString(fillStyle.Render(strings.Repeat(string(g.Full), full)))
	}
	used := full
	if rem > 0 && full < width {
		b.WriteString(fillStyle.Render(string(g.Partial[rem-1])))
		used++
	}
	if used < width {
		b.WriteString(t.BarEmpty.Render(strings.Repeat(string(g.Empty), width-used)))
	}
	return b.String()
}

// padRight pads s to width using display width, truncating when too long.
func padRight(s string, width int) string {
	w := lipgloss.Width(s)
	if w == width {
		return s
	}
	if w > width {
		return truncateDisplay(s, width)
	}
	return s + strings.Repeat(" ", width-w)
}

// padLeft pads s on the left to width.
func padLeft(s string, width int) string {
	w := lipgloss.Width(s)
	if w >= width {
		return truncateDisplay(s, width)
	}
	return strings.Repeat(" ", width-w) + s
}

// truncateDisplay shortens s to at most width display cells, adding an ellipsis
// when it had to cut.
func truncateDisplay(s string, width int) string {
	if width <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= width {
		return s
	}
	if width == 1 {
		return "…"
	}
	var b strings.Builder
	cur := 0
	for _, r := range s {
		rw := lipgloss.Width(string(r))
		if cur+rw > width-1 {
			break
		}
		b.WriteRune(r)
		cur += rw
	}
	return b.String() + "…"
}

// padCenter centers s within width.
func padCenter(s string, width int) string {
	w := lipgloss.Width(s)
	if w >= width {
		return truncateDisplay(s, width)
	}
	left := (width - w) / 2
	right := width - w - left
	return strings.Repeat(" ", left) + s + strings.Repeat(" ", right)
}

// wrapText breaks s on spaces so that no returned line exceeds width display
// cells. A word longer than width is emitted intact rather than split, because
// a half-word reads worse than an overflow the caller can trim.
func wrapText(s string, width int) []string {
	if width <= 0 {
		return nil
	}
	words := strings.Fields(s)
	if len(words) == 0 {
		return nil
	}
	var lines []string
	cur := words[0]
	for _, w := range words[1:] {
		if lipgloss.Width(cur)+1+lipgloss.Width(w) <= width {
			cur += " " + w
			continue
		}
		lines = append(lines, cur)
		cur = w
	}
	return append(lines, cur)
}

// writeWrapped writes s as an indented block, wrapped to width. Single-line
// text stays single-line, so this is safe to use everywhere a legend or note
// could run past a narrow terminal.
func writeWrapped(b *strings.Builder, s, indent string, width int) {
	for _, line := range wrapText(s, width-lipgloss.Width(indent)) {
		b.WriteString(indent)
		b.WriteString(line)
		b.WriteString("\n")
	}
}

// columns describes a table layout: fixed widths per column with a gap.
type columns struct {
	widths []int
	gap    int
}

// widthsFor distributes remaining space to a flexible column.
func (c columns) total() int {
	sum := 0
	for _, w := range c.widths {
		sum += w
	}
	return sum + c.gap*(len(c.widths)-1)
}

// fitWidths scales a layout so its total matches width, giving leftover space
// to the column at flexIdx. It never shrinks a column below its minimum.
func fitWidths(widths []int, minWidths []int, gap int, width int, flexIdx int) []int {
	out := make([]int, len(widths))
	copy(out, widths)
	total := gap * (len(widths) - 1)
	for _, w := range out {
		total += w
	}
	delta := width - total
	if flexIdx >= 0 && flexIdx < len(out) {
		out[flexIdx] += delta
		if minWidths != nil && flexIdx < len(minWidths) && out[flexIdx] < minWidths[flexIdx] {
			out[flexIdx] = minWidths[flexIdx]
		}
		// If the flex column hit its minimum, take the rest from the widest
		// non-flex columns so the table still fits.
		overflow := 0
		for _, w := range out {
			overflow += w
		}
		overflow += gap * (len(out) - 1)
		overflow -= width
		for overflow > 0 {
			widest, idx := 0, -1
			for i, w := range out {
				if i == flexIdx {
					continue
				}
				if w > widest && (minWidths == nil || i >= len(minWidths) || w > minWidths[i]) {
					widest, idx = w, i
				}
			}
			if idx < 0 {
				break
			}
			out[idx]--
			overflow--
		}
	}
	return out
}

// joinRow joins cells with the layout's gap.
func joinRow(cells []string, widths []int, gap int) string {
	parts := make([]string, len(cells))
	for i, c := range cells {
		w := 0
		if i < len(widths) {
			w = widths[i]
		}
		if w > 0 {
			parts[i] = padRight(c, w)
		} else {
			parts[i] = c
		}
	}
	return strings.Join(parts, strings.Repeat(" ", gap))
}
