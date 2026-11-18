// Package budget turns Console meters plus locally-declared per-model
// allocations into per-model remaining-budget figures.
//
// Two rules govern everything here:
//
//  1. The meter is the truth. Per-model costs from /api/usage/models are
//     attribution, and their sum under-reports the meter (rollups lag and the
//     meter counts differently). The gap is surfaced as Unattributed rather
//     than papered over by scaling.
//  2. Allocations are monthly. They are scaled into the 5-hour and weekly
//     windows by the ratio of those windows' live limits, so the operator
//     declares one number per model.
package budget

import (
	"math"
	"sort"
	"time"

	"github.com/cydnirn/opencode-limits/internal/allowance"
	"github.com/cydnirn/opencode-limits/internal/console"
)

// Window identifies one of the three Go budget windows.
type Window int

const (
	FiveHour Window = iota
	Week
	Month
)

// String returns the operator-facing name.
func (w Window) String() string {
	switch w {
	case FiveHour:
		return "5-hour"
	case Week:
		return "weekly"
	case Month:
		return "monthly"
	}
	return "unknown"
}

// Short returns the abbreviated label used in table headers.
func (w Window) Short() string {
	switch w {
	case FiveHour:
		return "5h"
	case Week:
		return "week"
	case Month:
		return "month"
	}
	return "?"
}

// All returns the windows in display order.
func All() []Window { return []Window{FiveHour, Week, Month} }

// MeterOf selects a window's meter from the grouped set.
func MeterOf(m console.Meters, w Window) console.Meter {
	switch w {
	case FiveHour:
		return m.FiveHour
	case Week:
		return m.Week
	default:
		return m.Month
	}
}

// CapSource records where a model's monthly cap came from, so the UI can say
// whether a number is the plan's own published limit or a local declaration.
type CapSource string

const (
	// CapFromPlan is the per-model monthly limit OpenCode publishes.
	CapFromPlan CapSource = "plan"
	// CapFromLocal is a model the published table does not cover, using the
	// operator's declared allocation instead.
	CapFromLocal CapSource = "local"
)

// resolveCap returns a model's monthly cap, whether it is unlimited, and where
// the number came from. The published plan figure wins over a local allocation
// because it is the number that actually applies.
func resolveCap(in Input, model string) (usd float64, unlimited bool, src CapSource, ok bool) {
	if l, found := in.Allowances.Lookup(model); found {
		if v, inf, has := l.MonthlyFor(in.Plan); has {
			return v, inf, CapFromPlan, true
		}
	}
	if v, has := in.Allocations[model]; has && v > 0 {
		return v, false, CapFromLocal, true
	}
	return 0, false, "", false
}

// Attribution is one model's spend inside a window.
type Attribution struct {
	Model   string
	Usage   console.ModelUsage
	Catalog console.CatalogModel
	// HasPricing reports that the catalog carried this model.
	HasPricing bool
	// AllocatedMonthly is the operator's declared monthly USD for this model.
	AllocatedMonthly float64
	HasAllocation    bool
	// AllocatedWindow is AllocatedMonthly scaled into this window.
	AllocatedWindow console.MicroCents
	// Remaining is AllocatedWindow minus spend, floored at zero.
	Remaining console.MicroCents
	// Exhausted reports that spend met or exceeded the window allocation.
	Exhausted bool
	// Unlimited reports that the plan places no monthly cap on this model.
	Unlimited bool
	// CapSource says where the cap came from: the plan's published table or a
	// locally declared allocation.
	CapSource CapSource
	// PercentOfAllocation is spend/allocation, PercentOfPlan is spend/limit.
	// Both are 0 when the denominator is zero.
	PercentOfAllocation float64
	PercentOfPlan       float64
	// BurnRatePerHour is spend divided by hours elapsed in the window.
	BurnRatePerHour float64
	// ETA is the projected time until remaining hits zero; zero when unbounded.
	ETA time.Duration
	// ETAExceedsWindow reports the model will not run dry before the reset.
	ETAExceedsWindow bool
}

// Spend returns the model's spend in the window.
func (a Attribution) Spend() console.MicroCents { return a.Usage.CostMicroCents }

// WindowView is one budget window fully resolved.
type WindowView struct {
	Window Window
	Meter  console.Meter
	// Models are attributed rows for this window, sorted by remaining
	// ascending so the model closest to running dry leads.
	Models []Attribution
	// Attributed is the sum of per-model spend.
	Attributed console.MicroCents
	// Unattributed is meter used minus attributed, which may be negative when
	// rollups trail the meter. It is shown, never redistributed.
	Unattributed console.MicroCents
	// UnattributedPercent is Unattributed as a share of meter used.
	UnattributedPercent float64
	// AllocatedTotal is the sum of every model's cap scaled into this window. It
	// is informational for plan caps, which are not shares of a shared pool.
	AllocatedTotal console.MicroCents
	// HasPlanCaps reports that at least one model's cap came from the plan's
	// published table rather than a local declaration.
	HasPlanCaps bool
	// OverAllocated is AllocatedTotal minus the window limit, floored at zero. It
	// is only meaningful when every cap is a local share (HasPlanCaps false),
	// because plan caps are independent rather than additive.
	OverAllocated console.MicroCents
	Now           time.Time
}

// PercentUsed returns the meter's percentage and whether a limit exists.
func (v WindowView) PercentUsed() (float64, bool) { return v.Meter.Percent() }

// TimeToReset returns the duration until the window resets.
func (v WindowView) TimeToReset() time.Duration {
	if v.Meter.ResetsAt.IsZero() {
		return 0
	}
	d := v.Meter.ResetsAt.Sub(v.Now)
	if d < 0 {
		return 0
	}
	return d
}

// Elapsed returns how much of the window has passed, and its total length.
func (v WindowView) Elapsed() (time.Duration, time.Duration) {
	if v.Meter.StartsAt.IsZero() || v.Meter.ResetsAt.IsZero() {
		return 0, 0
	}
	total := v.Meter.ResetsAt.Sub(v.Meter.StartsAt)
	el := v.Now.Sub(v.Meter.StartsAt)
	if el < 0 {
		el = 0
	}
	if el > total {
		el = total
	}
	return el, total
}

// Input carries everything needed to compute views.
type Input struct {
	Now    time.Time
	Meters console.Meters
	// Plan is the subscription tier ("go", "go-plus", "go-teams"); it selects
	// which column of the published per-model allowance applies.
	Plan string
	// Allowances is the published monthly cap per model. These are the plan's
	// real per-model limits and take precedence over Allocations.
	Allowances allowance.Table
	// Allocations is the legacy operator-declared overlay, kept for models the
	// published table does not cover.
	Allocations map[string]float64
	// UsageByWindow holds per-model usage fetched with each window's own
	// startsAt, which is what makes per-window attribution possible.
	UsageByWindow map[Window][]console.ModelUsage
	Catalog       map[string]console.CatalogModel
}

// Build resolves every window.
func Build(in Input) []WindowView {
	views := make([]WindowView, 0, len(All()))
	for _, w := range All() {
		views = append(views, buildWindow(in, w))
	}
	return views
}

func buildWindow(in Input, w Window) WindowView {
	meter := MeterOf(in.Meters, w)
	v := WindowView{Window: w, Meter: meter, Now: in.Now}

	scale := windowScale(in.Meters, w)
	elapsed, _ := v.Elapsed()
	usage := in.UsageByWindow[w]

	rows := make([]Attribution, 0, len(usage))
	var attributed console.MicroCents
	var allocatedTotal console.MicroCents
	var hasPlanCaps bool

	for _, u := range usage {
		a := Attribution{Model: u.Model, Usage: u}
		if cm, ok := in.Catalog[u.Model]; ok {
			a.Catalog = cm
			a.HasPricing = true
		}
		// The published per-model monthly cap is the plan's own limit and wins;
		// a declared allocation is only a fallback for models it does not cover.
		if capUSD, unlimited, source, ok := resolveCap(in, u.Model); ok {
			a.HasAllocation = true
			a.AllocatedMonthly = capUSD
			a.CapSource = source
			if source == CapFromPlan {
				hasPlanCaps = true
			}
			if unlimited {
				a.Unlimited = true
				// No ceiling: show spend, never claim a remaining amount.
				a.Remaining = 0
			} else {
				a.AllocatedWindow = console.FromUSD(capUSD * scale)
				allocatedTotal += a.AllocatedWindow
				spend := u.CostMicroCents
				if spend < a.AllocatedWindow {
					a.Remaining = a.AllocatedWindow - spend
				} else {
					a.Exhausted = true
				}
				if a.AllocatedWindow > 0 {
					a.PercentOfAllocation = float64(spend) / float64(a.AllocatedWindow) * 100
				}
			}
		}
		if meter.LimitMicroCents > 0 {
			a.PercentOfPlan = float64(u.CostMicroCents) / float64(meter.LimitMicroCents) * 100
		}
		// Burn rate and ETA only mean something once time has passed and there
		// is spend to extrapolate from.
		if elapsed > 0 && u.CostMicroCents > 0 {
			a.BurnRatePerHour = u.CostMicroCents.USD() / elapsed.Hours()
			if a.HasAllocation && !a.Exhausted && a.BurnRatePerHour > 0 {
				// Remaining USD / USD-per-hour = hours until dry.
				hoursLeft := a.Remaining.USD() / a.BurnRatePerHour
				eta := time.Duration(hoursLeft * float64(time.Hour))
				if meter.ResetsAt.IsZero() || !in.Now.Add(eta).After(meter.ResetsAt) {
					a.ETA = eta
				} else {
					a.ETAExceedsWindow = true
				}
			}
		}
		attributed += u.CostMicroCents
		rows = append(rows, a)
	}

	// Models with an allocation but no usage still hold budget, and the
	// operator needs to see them as fully available rather than missing.
	seen := make(map[string]bool, len(rows))
	for _, r := range rows {
		seen[r.Model] = true
	}
	// Every model with a cap should be listed even before it is used, so the
	// operator can see what is available rather than only what has been spent.
	unused := map[string]bool{}
	for model := range in.Allocations {
		unused[model] = true
	}
	for _, model := range in.Allowances.ModelIDs() {
		unused[model] = true
	}
	for model := range unused {
		if seen[model] {
			continue
		}
		capUSD, unlimited, source, ok := resolveCap(in, model)
		if !ok {
			continue
		}
		a := Attribution{
			Model:            model,
			HasAllocation:    true,
			AllocatedMonthly: capUSD,
			CapSource:        source,
			Unlimited:        unlimited,
		}
		if source == CapFromPlan {
			hasPlanCaps = true
		}
		if !unlimited {
			a.AllocatedWindow = console.FromUSD(capUSD * scale)
			a.Remaining = a.AllocatedWindow
			allocatedTotal += a.AllocatedWindow
		}
		if cm, ok := in.Catalog[model]; ok {
			a.Catalog = cm
			a.HasPricing = true
		}
		rows = append(rows, a)
	}

	sortAttributions(rows)

	v.Models = rows
	v.Attributed = attributed
	v.Unattributed = meter.UsedMicroCents - attributed
	if meter.UsedMicroCents > 0 {
		v.UnattributedPercent = float64(v.Unattributed) / float64(meter.UsedMicroCents) * 100
	}
	v.AllocatedTotal = allocatedTotal
	v.HasPlanCaps = hasPlanCaps
	// Over-allocation only means anything for locally declared shares of one pool.
	// The plan's published per-model caps are independent ceilings: every model may
	// spend up to its own cap regardless of the others, so summing them and
	// comparing to the plan meter (e.g. $555 of caps against a $60 monthly meter)
	// is meaningless and reported nothing meaningful before.
	if !v.HasPlanCaps && allocatedTotal > meter.LimitMicroCents {
		v.OverAllocated = allocatedTotal - meter.LimitMicroCents
	}
	return v
}

// sortAttributions orders rows so the model nearest exhaustion leads:
// allocated models by remaining ascending, then unallocated models by spend
// descending, so the expensive undeclared ones are still visible up top.
func sortAttributions(rows []Attribution) {
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		// Unlimited models cannot run out, so they never belong at the top of a
		// "what is about to run dry" list.
		if a.Unlimited != b.Unlimited {
			return !a.Unlimited
		}
		if a.HasAllocation != b.HasAllocation {
			return a.HasAllocation
		}
		if a.HasAllocation {
			if a.Remaining != b.Remaining {
				return a.Remaining < b.Remaining
			}
			return a.Spend() > b.Spend()
		}
		if a.Spend() != b.Spend() {
			return a.Spend() > b.Spend()
		}
		return a.Model < b.Model
	})
}

// windowScale returns the factor that maps a monthly allocation into window w.
//
// The scale is the window's live limit over the monthly limit, so a plan whose
// windows are 12/30/60 uses 0.2 and 0.5. When a limit is unknown the published
// plan defaults fill in; failing everything, the allocation is treated as
// already expressed in the window.
func windowScale(m console.Meters, w Window) float64 {
	if w == Month {
		return 1
	}
	monthLimit := m.Month.LimitMicroCents
	if monthLimit <= 0 {
		monthLimit = DefaultPlanLimits.Month.LimitMicroCents
	}
	winLimit := MeterOf(m, w).LimitMicroCents
	if winLimit <= 0 {
		winLimit = MeterOf(DefaultPlanLimits, w).LimitMicroCents
	}
	if monthLimit <= 0 || winLimit <= 0 {
		return 1
	}
	return float64(winLimit) / float64(monthLimit)
}

// DefaultPlanLimits is the published Go plan allowance, used only when a live
// meter is absent. Values match the Console's own plan table.
var DefaultPlanLimits = console.Meters{
	FiveHour: console.Meter{LimitMicroCents: 1_200_000_000},
	Week:     console.Meter{LimitMicroCents: 3_000_000_000},
	Month:    console.Meter{LimitMicroCents: 6_000_000_000},
}

// PlanLimits maps a product identifier to its published allowance.
func PlanLimits(product string) console.Meters {
	switch product {
	case "go-plus":
		return console.Meters{
			FiveHour: console.Meter{LimitMicroCents: 4_800_000_000},
			Week:     console.Meter{LimitMicroCents: 12_000_000_000},
			Month:    console.Meter{LimitMicroCents: 24_000_000_000},
		}
	case "go-teams":
		return console.Meters{
			FiveHour: console.Meter{LimitMicroCents: 1_200_000_000},
			Week:     console.Meter{LimitMicroCents: 3_000_000_000},
			Month:    console.Meter{LimitMicroCents: 6_000_000_000},
		}
	default:
		return DefaultPlanLimits
	}
}

// FormatUSD renders microCents as a dollar string with two decimals.
func FormatUSD(m console.MicroCents) string {
	v := m.USD()
	neg := v < 0
	if neg {
		v = -v
	}
	s := FormatFloat(v, 2)
	if neg {
		return "-$" + s
	}
	return "$" + s
}

// FormatFloat renders v with a fixed number of decimals, without fmt.
func FormatFloat(v float64, decimals int) string {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return "0." + zeros(decimals)
	}
	pow := math.Pow(10, float64(decimals))
	r := math.Round(v*pow) / pow
	whole := int64(r)
	frac := r - float64(whole)
	fs := int64(math.Round(frac * pow))
	if fs >= int64(pow) {
		whole++
		fs -= int64(pow)
	}
	ws := Itoa(whole)
	if decimals == 0 {
		return ws
	}
	fstr := Itoa(fs)
	for len(fstr) < decimals {
		fstr = "0" + fstr
	}
	return ws + "." + fstr
}

func zeros(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = '0'
	}
	return string(b)
}

// Itoa renders an int64 without fmt.
func Itoa(v int64) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var b [20]byte
	i := len(b)
	for v > 0 {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

// FormatDuration renders a duration compactly, as "3h 12m" or "4d 14h".
func FormatDuration(d time.Duration) string {
	if d <= 0 {
		return "now"
	}
	mins := int64(d.Minutes() + 0.5)
	if mins < 1 {
		return "<1m"
	}
	h := mins / 60
	m := mins % 60
	if h >= 24 {
		return Itoa(h/24) + "d " + Itoa(h%24) + "h"
	}
	if h == 0 {
		return Itoa(m) + "m"
	}
	return Itoa(h) + "h " + Itoa(m) + "m"
}

// FormatPercent renders a percentage with one decimal below 10 and none above,
// which keeps table columns narrow without losing meaning.
//
// Values at or above 100 keep their magnitude up to 999%; clamping everything to
// "100%" hid real overspend (the live 5-hour meter hit 875% of its limit while
// rendering as "100%"). Beyond 999% the exact figure matters less than the fact
// that it is wildly over, so it saturates.
func FormatPercent(p float64) string {
	if math.IsNaN(p) {
		return "--"
	}
	if p >= 1000 {
		return "999%+"
	}
	if p >= 100 {
		return Itoa(int64(math.Round(p))) + "%"
	}
	if p < 0 {
		p = 0
	}
	if p < 10 {
		return FormatFloat(p, 1) + "%"
	}
	return Itoa(int64(math.Round(p))) + "%"
}

// FormatTokens renders a token count with a K/M/B suffix.
func FormatTokens(n int64) string {
	switch {
	case n >= 1_000_000_000:
		return FormatFloat(float64(n)/1e9, 1) + "B"
	case n >= 1_000_000:
		return FormatFloat(float64(n)/1e6, 1) + "M"
	case n >= 1_000:
		return FormatFloat(float64(n)/1e3, 1) + "K"
	default:
		return Itoa(n)
	}
}
