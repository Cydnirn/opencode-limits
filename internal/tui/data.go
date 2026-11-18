package tui

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/cydnirn/opencode-limits/internal/allowance"
	"github.com/cydnirn/opencode-limits/internal/budget"
	"github.com/cydnirn/opencode-limits/internal/config"
	"github.com/cydnirn/opencode-limits/internal/console"
)

// Snapshot is everything one refresh produces. Views render from this alone, so
// rendering is pure and testable from a literal.
type Snapshot struct {
	FetchedAt time.Time
	Now       time.Time

	Status   console.GoStatus
	Billing  console.BillingStatus
	Catalog  map[string]console.CatalogModel
	Enabled  []console.EnabledModel
	Windows  []budget.WindowView
	Activity console.ActivityGo

	// Allocations echoes the config in force, so the UI can distinguish "no
	// allocation declared" from "allocation is zero".
	Allocations map[string]float64
	// Allowances is the published per-model monthly limit table in force.
	Allowances allowance.Table
	// AllowanceSource says whether that table came from the binary or a refresh.
	AllowanceSource allowance.Source
	// Plan is the subscription tier, which selects the allowance column.
	Plan     string
	PlanName string
	OrgName  string
	Email    string

	// Err records a partial failure: some data loaded, some did not. The UI
	// shows what it has and says what is stale.
	Err error

	// configPath is where allocations live, surfaced in empty states.
	configPath string
}

// Window returns the view for a window.
func (s Snapshot) Window(w budget.Window) budget.WindowView {
	for _, v := range s.Windows {
		if v.Window == w {
			return v
		}
	}
	return budget.WindowView{Window: w}
}

// ModelName resolves a display name from the catalog, falling back to the ID.
func (s Snapshot) ModelName(id string) string {
	if cm, ok := s.Catalog[id]; ok && cm.Name != "" {
		return cm.Name
	}
	for _, e := range s.Enabled {
		if e.ID == id {
			return e.Name
		}
	}
	return id
}

// Loader fetches everything a Snapshot needs.
type Loader struct {
	Client *console.Client
	Config config.Config
	// ConfigPath is echoed into empty states so the operator knows where to
	// declare allocations.
	ConfigPath string
	// Allowances supplies the plan's published per-model monthly limits. When nil
	// the embedded table is used.
	Allowances *allowance.Resolver
	// Timeout bounds the whole refresh so a hung endpoint cannot wedge the UI.
	Timeout time.Duration
}

// Load fetches all data. Partial failures are collected into Snapshot.Err
// rather than aborting, because the meters alone are still worth showing.
func (l Loader) Load(ctx context.Context) Snapshot {
	if l.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, l.Timeout)
		defer cancel()
	}
	now := time.Now().UTC()
	snap := Snapshot{
		FetchedAt:   now,
		Now:         now,
		Catalog:     map[string]console.CatalogModel{},
		Allocations: l.Config.Allocations,
		configPath:  l.ConfigPath,
	}
	if snap.Allocations == nil {
		snap.Allocations = map[string]float64{}
	}
	if l.Client == nil {
		snap.Err = errors.New("no API client")
		return snap
	}

	var errs []error

	status, err := l.Client.GoStatus()
	if err != nil {
		// Without meters there is nothing meaningful to show.
		snap.Err = fmt.Errorf("fetch plan status: %w", err)
		return snap
	}
	snap.Status = status
	snap.PlanName = productName(status.Product)
	if cred := l.Client.Credential(); cred.Email != "" {
		snap.Email = cred.Email
		snap.OrgName = cred.OrgName
	}

	// Per-window per-model usage: three calls, each with its own window start,
	// which is what makes per-model remaining budget possible at all.
	usageByWindow := map[budget.Window][]console.ModelUsage{}
	if status.Access != nil {
		for _, w := range budget.All() {
			meter := budget.MeterOf(status.Access.Meters, w)
			since := meter.StartsAt
			if since.IsZero() {
				// The monthly meter carries no startsAt; the period start does.
				since = status.Access.StartsAt
			}
			rows, err := l.Client.UsageSince(since)
			if err != nil {
				errs = append(errs, fmt.Errorf("usage for %s window: %w", w, err))
				continue
			}
			usageByWindow[w] = rows
		}
	}

	if cat, err := l.Client.Catalog(); err != nil {
		errs = append(errs, fmt.Errorf("fetch catalog: %w", err))
	} else {
		snap.Catalog = cat
	}

	if en, err := l.Client.SubscriptionModels(); err != nil {
		errs = append(errs, fmt.Errorf("fetch plan models: %w", err))
	} else {
		snap.Enabled = en
	}

	if bill, err := l.Client.Billing(); err != nil {
		errs = append(errs, fmt.Errorf("fetch billing: %w", err))
	} else {
		snap.Billing = bill
	}

	if act, err := l.Client.Activity(); err != nil {
		errs = append(errs, fmt.Errorf("fetch activity: %w", err))
	} else {
		snap.Activity = act
	}

	var meters console.Meters
	if status.Access != nil {
		meters = status.Access.Meters
	}
	// Resolve the published per-model limits before building the views: the
	// embedded table answers immediately and a refresh replaces it if it can.
	resolver := l.Allowances
	if resolver == nil {
		resolver = &allowance.Resolver{}
	}
	table, source := resolver.Get(ctx)
	snap.Allowances = table
	snap.AllowanceSource = source
	snap.Plan = planKey(status.Product)

	snap.Windows = budget.Build(budget.Input{
		Now:           now,
		Meters:        meters,
		Plan:          snap.Plan,
		Allowances:    table,
		Allocations:   snap.Allocations,
		UsageByWindow: usageByWindow,
		Catalog:       snap.Catalog,
	})

	if len(errs) > 0 {
		snap.Err = errors.Join(errs...)
	}
	return snap
}

// planKey maps a product identifier to the allowance table's plan column.
func planKey(product string) string {
	switch product {
	case "go-plus":
		return "go-plus"
	case "go-teams":
		return "go-teams"
	default:
		return "go"
	}
}

// productName maps a product identifier to a display name.
func productName(p string) string {
	switch p {
	case "go":
		return "OpenCode Go"
	case "go-plus":
		return "OpenCode Go Plus"
	case "go-teams":
		return "OpenCode Go Teams"
	case "":
		return "OpenCode"
	default:
		return p
	}
}

// totalOf sums a window's meter used across models, for summary lines.
func totalOf(rows []budget.Attribution) console.MicroCents {
	var t console.MicroCents
	for _, r := range rows {
		t += r.Spend()
	}
	return t
}
