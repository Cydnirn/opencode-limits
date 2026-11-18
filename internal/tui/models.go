package tui

import (
	"fmt"
	"sort"
	"strings"

	"github.com/cydnirn/opencode-limits/internal/budget"
	"github.com/cydnirn/opencode-limits/internal/console"
)

// sortMode selects the Models-screen ordering.
type sortMode int

const (
	sortByRemaining sortMode = iota
	sortBySpend
	sortByRequests
	sortByTokens
	sortModeCount
)

// String names the sort for the footer.
func (m sortMode) String() string {
	switch m {
	case sortByRemaining:
		return "remaining"
	case sortBySpend:
		return "spend"
	case sortByRequests:
		return "requests"
	case sortByTokens:
		return "tokens"
	}
	return "?"
}

// next returns the following sort mode.
func (m sortMode) next() sortMode { return (m + 1) % sortModeCount }

// renderModels draws detailed per-model usage across all three windows: token
// classes, cost, allocation, remaining, and effective rate.
func renderModels(t Theme, s Snapshot, width int, mode sortMode, warning, danger int) string {
	var b strings.Builder

	b.WriteString(t.Header.Render("MODEL DETAIL"))
	b.WriteString(t.Dim.Render(fmt.Sprintf("   sorted by %s  (s cycles)", mode)))
	b.WriteString("\n\n")

	// Gather every model that appears in any window or has an allocation.
	type row struct {
		id      string
		name    string
		usage   console.ModelUsage // from the focused window
		monthly budget.Attribution
		inFive  budget.Attribution
	}
	rows := map[string]*row{}
	focus := s.FocusWindow()

	for _, w := range s.Windows {
		for _, a := range w.Models {
			r, ok := rows[a.Model]
			if !ok {
				r = &row{id: a.Model, name: s.ModelName(a.Model)}
				rows[a.Model] = r
			}
			switch w.Window {
			case focus:
				r.usage = a.Usage
			case budget.Month:
				r.monthly = a
			case budget.FiveHour:
				r.inFive = a
			}
		}
	}
	for id := range s.Allocations {
		if _, ok := rows[id]; !ok {
			rows[id] = &row{id: id, name: s.ModelName(id)}
		}
	}

	list := make([]*row, 0, len(rows))
	for _, r := range rows {
		list = append(list, r)
	}
	sortRows(list, mode, func(r *row) sortKey {
		return sortKey{
			remaining: r.monthly.Remaining,
			hasAlloc:  r.monthly.HasAllocation,
			spend:     r.monthly.Spend(),
			requests:  r.usage.Requests,
			tokens:    r.usage.TotalTokens(),
		}
	})

	// Columns sized for the terminal.
	const (
		modelW = 24
		numW   = 8
		tokW   = 8
		costW  = 9
		leftW  = 9
		ctxW   = 8
		gap    = 2
	)
	header := []string{
		padRight("MODEL", modelW),
		padLeft("REQS", numW),
		padLeft("INPUT", tokW),
		padLeft("OUTPUT", tokW),
		padLeft("CACHE-R", tokW),
		padLeft("COST", costW),
		padLeft("$/1M", costW),
		padLeft("LEFT(MO)", leftW),
		padLeft("%PLAN", 6),
		padLeft("CONTEXT", ctxW),
	}
	if width < 110 {
		// Drop the token-class columns before dropping model names or money.
		header = []string{
			padRight("MODEL", modelW),
			padLeft("REQS", numW),
			padLeft("COST", costW),
			padLeft("$/1M", costW),
			padLeft("LEFT(MO)", leftW),
			padLeft("%PLAN", 6),
		}
	}
	b.WriteString(t.Dim.Render("  " + strings.Join(header, strings.Repeat(" ", gap))))
	b.WriteString("\n")

	for _, r := range list {
		cells := []string{padRight(r.name, modelW)}
		if width < 110 {
			cells = append(cells,
				padLeft(budget.Itoa(r.usage.Requests), numW),
				padLeft(budget.FormatUSD(r.usage.CostMicroCents), costW),
				padLeft(ratePerM(r.usage), costW),
			)
		} else {
			cells = append(cells,
				padLeft(budget.Itoa(r.usage.Requests), numW),
				padLeft(budget.FormatTokens(r.usage.InputTokens), tokW),
				padLeft(budget.FormatTokens(r.usage.OutputTokens), tokW),
				padLeft(budget.FormatTokens(r.usage.CacheRead), tokW),
				padLeft(budget.FormatUSD(r.usage.CostMicroCents), costW),
				padLeft(ratePerM(r.usage), costW),
			)
		}

		leftText := "--"
		leftStyle := t.Dim
		if r.monthly.HasAllocation {
			leftText = budget.FormatUSD(r.monthly.Remaining)
			switch {
			case r.monthly.Exhausted:
				leftStyle = t.Bad
			case r.monthly.PercentOfAllocation >= float64(warning):
				leftStyle = t.Warn
			default:
				leftStyle = t.Good
			}
		}
		cells = append(cells, leftStyle.Render(padLeft(leftText, leftW)))
		cells = append(cells, padLeft(budget.FormatPercent(r.monthly.PercentOfPlan), 6))

		if width >= 110 {
			ctx := "--"
			if cm, ok := s.Catalog[r.id]; ok && cm.ContextLimit > 0 {
				ctx = budget.FormatTokens(cm.ContextLimit)
			}
			cells = append(cells, padLeft(ctx, ctxW))
		}

		line := "  " + strings.Join(cells, strings.Repeat(" ", gap))
		b.WriteString(strings.TrimRight(line, " "))
		b.WriteString("\n")
	}

	b.WriteString("\n")
	writeWrapped(&b, fmt.Sprintf(
		"token and cost columns are for the %s window; LEFT/%%PLAN are monthly allocations",
		focus.String()), "  ", width)
	return b.String()
}

// ratePerM renders the blended cost per million tokens for a row.
func ratePerM(u console.ModelUsage) string {
	r := u.CostPerMTok()
	if r == 0 {
		if u.TotalTokens() == 0 {
			return "--"
		}
		return "FREE"
	}
	return "$" + budget.FormatFloat(r, 2)
}

// sortKey carries every field the sort modes rank on, so one comparator can
// serve all of them without the sentinel tricks that made unallocated models
// sort to the top of "remaining".
type sortKey struct {
	remaining console.MicroCents
	hasAlloc  bool
	spend     console.MicroCents
	requests  int64
	tokens    int64
}

// sortRows orders model rows by the active mode. Unallocated models always sort
// after allocated ones under "remaining", because a model with no declared
// budget is not a candidate for running out of it.
func sortRows[T any](rows []T, mode sortMode, key func(T) sortKey) {
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := key(rows[i]), key(rows[j])
		switch mode {
		case sortByRemaining:
			if a.hasAlloc != b.hasAlloc {
				return a.hasAlloc
			}
			if a.remaining != b.remaining {
				return a.remaining < b.remaining
			}
			return a.spend > b.spend
		case sortBySpend:
			if a.spend != b.spend {
				return a.spend > b.spend
			}
		case sortByRequests:
			if a.requests != b.requests {
				return a.requests > b.requests
			}
		case sortByTokens:
			if a.tokens != b.tokens {
				return a.tokens > b.tokens
			}
		}
		return false
	})
}

// renderPricing draws the static price list: per-1M rates, context-override
// tiers, time multipliers, and limits. Sorted by output price so the cheap and
// expensive ends are obvious.
func renderPricing(t Theme, s Snapshot, width int, warning, danger int) string {
	var b strings.Builder

	b.WriteString(t.Header.Render("MODEL PRICING"))
	b.WriteString(t.Dim.Render("   USD per 1M tokens, from the workspace catalog"))
	b.WriteString("\n\n")

	ids := sortedModelIDs(s)
	sort.SliceStable(ids, func(i, j int) bool {
		a, b2 := s.Catalog[ids[i]], s.Catalog[ids[j]]
		if a.OutputPerMTok != b2.OutputPerMTok {
			return a.OutputPerMTok < b2.OutputPerMTok
		}
		return ids[i] < ids[j]
	})

	const (
		modelW  = 26
		priceW  = 8
		limitW  = 9
		noteMin = 10
		gap     = 2
	)

	// Column set follows the room available: the notes column goes first, then
	// the cache-write tier, then the cache-read tier. Input price, output price
	// and the two token limits are the minimum useful pricing story.
	showCacheW := width >= 96
	showCacheR := width >= 84

	// Widths are assembled once so the header, every row and the leftover
	// notes column are guaranteed to agree on the total.
	widths := []int{modelW, priceW}
	if showCacheR {
		widths = append(widths, priceW)
	}
	if showCacheW {
		widths = append(widths, priceW)
	}
	widths = append(widths, priceW, limitW, limitW) // output, context, max-out

	used := 2 + gap*len(widths) // left margin plus a gap after every column
	for _, w := range widths {
		used += w
	}
	noteW := width - used
	if noteW < noteMin {
		// No room for notes: keep the warning attached to its model instead.
		noteW = 0
	} else {
		widths = append(widths, noteW)
	}

	header := []string{padRight("MODEL", modelW), padLeft("INPUT", priceW)}
	if showCacheR {
		header = append(header, padLeft("CACHE-R", priceW))
	}
	if showCacheW {
		header = append(header, padLeft("CACHE-W", priceW))
	}
	header = append(header,
		padLeft("OUTPUT", priceW),
		padLeft("CONTEXT", limitW),
		padLeft("MAX-OUT", limitW),
	)
	if noteW > 0 {
		header = append(header, padRight("NOTES", noteW))
	}
	b.WriteString(t.Dim.Render("  " + strings.Join(header, strings.Repeat(" ", gap))))
	b.WriteString("\n")

	for _, id := range ids {
		cm := s.Catalog[id]
		name := cm.Name
		if name == "" {
			name = id
		}
		price := func(v float64) string {
			if v == 0 {
				return "free"
			}
			return "$" + budget.FormatFloat(v, 2)
		}
		ctx, out := "--", "--"
		if cm.ContextLimit > 0 {
			ctx = budget.FormatTokens(cm.ContextLimit)
		}
		if cm.OutputLimit > 0 {
			out = budget.FormatTokens(cm.OutputLimit)
		}

		var notes []string
		if cm.HasContextOverride {
			notes = append(notes, fmt.Sprintf(">%s ctx: $%.2f in",
				budget.FormatTokens(cm.OverrideAtContext), cm.OverrideInputRate))
		}
		if cm.HasTimeMultiplier {
			notes = append(notes, multiplierNote(cm))
		}
		noteText := strings.Join(notes, "; ")
		if cm.Free() {
			noteText = "no charge"
		}

		cells := []string{padRight(name, modelW), padLeft(price(cm.InputPerMTok), priceW)}
		if showCacheR {
			cells = append(cells, padLeft(price(cm.CacheReadPerMTok), priceW))
		}
		if showCacheW {
			cells = append(cells, padLeft(price(cm.CacheWritePerMTok), priceW))
		}
		cells = append(cells,
			padLeft(price(cm.OutputPerMTok), priceW),
			padLeft(ctx, limitW),
			padLeft(out, limitW),
		)
		if noteW > 0 {
			cells = append(cells, padRight(truncateDisplay(noteText, noteW), noteW))
		}
		line := "  " + strings.Join(cells, strings.Repeat(" ", gap))
		b.WriteString(strings.TrimRight(line, " "))
		b.WriteString("\n")

		// Without a notes column the override tier is still worth showing, so
		// it moves under its own model rather than disappearing.
		if noteW == 0 && noteText != "" {
			writeWrapped(&b, noteText, "    ", width)
		}
	}

	b.WriteString("\n")
	writeWrapped(&b, "Peak pricing is the factor the catalog advertises, applied during the listed window.", "  ", width)
	writeWrapped(&b, "Cache-W shows the cheapest cache-write tier when a model prices tiers apart.", "  ", width)
	return b.String()
}

// multiplierNote renders the advertised peak-price factor and when it applies.
// The factor comes from the catalog rather than being assumed, because the live
// data uses different shapes (weekday peak windows and start dates) and more
// than one factor value.
func multiplierNote(cm console.CatalogModel) string {
	factor := cm.MultiplierFactor
	if factor <= 0 {
		return "peak pricing (see catalog)"
	}
	label := "x" + budget.FormatFloat(factor, 1)
	switch {
	case len(cm.MultiplierDays) > 0:
		return fmt.Sprintf("%s on %s UTC", label, strings.Join(cm.MultiplierDays, ","))
	case cm.MultiplierFromDate != "":
		return fmt.Sprintf("%s from %s", label, cm.MultiplierFromDate)
	default:
		return label + " peak"
	}
}
