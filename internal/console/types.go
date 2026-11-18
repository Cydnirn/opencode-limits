// Package console is a typed client for the OpenCode Console API.
//
// It authenticates with the OAuth credential written by the opencode-console
// CLI and refreshes it in place. Refresh tokens ROTATE on every use, so a
// refreshed credential must be written back or every other tool sharing the
// file is logged out.
package console

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"time"
)

// Credential mirrors the JSON shape written by the opencode-console CLI at
// <data-dir>/opencode-console/credential.json. Keys this struct does not model
// are preserved on write by re-marshalling the original document alongside
// these fields.
type Credential struct {
	Type      string `json:"type"`
	Server    string `json:"server"`
	Access    string `json:"access"`
	Refresh   string `json:"refresh"`
	Expires   int64  `json:"expires"` // unix milliseconds
	AccountID string `json:"accountID"`
	Email     string `json:"email"`
	OrgID     string `json:"orgID"`
	OrgName   string `json:"orgName"`
}

// Expired reports whether the access token is at or past its expiry, with a
// safety margin so a request is never sent with a token that dies in flight.
func (c Credential) Expired(now time.Time) bool {
	if c.Expires == 0 {
		return false
	}
	skew := 60 * time.Second
	return !now.Add(skew).Before(time.UnixMilli(c.Expires))
}

// Meter is one budget window's state. All currency values are microCents,
// where 1 USD = 100,000,000 microCents.
type Meter struct {
	StartsAt        time.Time
	ResetsAt        time.Time
	LimitMicroCents MicroCents
	UsedMicroCents  MicroCents
}

// MicroCents is USD * 1e8. int64 covers roughly 92 billion USD, which is far
// beyond any realistic spend, and is much easier to do arithmetic on than
// big.Int.
type MicroCents int64

// MicroCentsPerUSD is the fixed conversion factor.
const MicroCentsPerUSD = 100_000_000

// USD converts to dollars for display.
func (m MicroCents) USD() float64 { return float64(m) / MicroCentsPerUSD }

// String renders the value as a dollar amount.
func (m MicroCents) String() string { return fmt.Sprintf("$%.2f", m.USD()) }

// FromUSD converts dollars to microCents, rounding half away from zero so
// negative values survive a round trip (int64 truncation would skew them).
func FromUSD(usd float64) MicroCents { return MicroCents(math.Round(usd * MicroCentsPerUSD)) }

// Percent returns used/limit as a percentage, and false when the limit is zero.
func (m Meter) Percent() (float64, bool) {
	if m.LimitMicroCents <= 0 {
		return 0, false
	}
	return float64(m.UsedMicroCents) / float64(m.LimitMicroCents) * 100, true
}

// Remaining returns limit minus used, floored at zero.
func (m Meter) Remaining() MicroCents {
	r := m.LimitMicroCents - m.UsedMicroCents
	if r < 0 {
		return 0
	}
	return r
}

// Exhausted reports whether the window's limit has been reached.
//
// A zero limit means no data, not a spent budget: reporting exhaustion there
// would light up "LIMIT REACHED" on a meter the API simply did not populate.
func (m Meter) Exhausted() bool {
	return m.LimitMicroCents > 0 && m.UsedMicroCents >= m.LimitMicroCents
}

// HasLimit reports whether the meter carries a usable limit.
func (m Meter) HasLimit() bool { return m.LimitMicroCents > 0 }

// Meters groups the three Go budget windows.
type Meters struct {
	FiveHour Meter
	Week     Meter
	Month    Meter
}

// GoStatus is the payload of GET /api/go/status.
type GoStatus struct {
	SubscriberUserID string
	Product          string
	RenewalProduct   string
	Access           *GoAccess
}

// GoAccess is the subscription period plus its meters.
type GoAccess struct {
	StartsAt    time.Time
	EndsAt      time.Time
	CancelAtEnd bool
	Meters      Meters
}

// ModelUsage is one row of GET /api/usage/models — per-model usage since a
// timestamp, which is how per-window per-model spend is derived.
type ModelUsage struct {
	Model          string
	Provider       string
	Requests       int64
	InputTokens    int64
	OutputTokens   int64
	CacheRead      int64
	CacheWrite5m   int64
	CacheWrite1h   int64
	CostMicroCents MicroCents
}

// TotalTokens is the sum of every token class.
func (m ModelUsage) TotalTokens() int64 {
	return m.InputTokens + m.OutputTokens + m.CacheRead + m.CacheWrite5m + m.CacheWrite1h
}

// CostPerMTok returns the blended cost per million total tokens.
func (m ModelUsage) CostPerMTok() float64 {
	t := m.TotalTokens()
	if t <= 0 {
		return 0
	}
	return m.CostMicroCents.USD() / float64(t) * 1e6
}

// EnabledModel is a model the plan permits.
type EnabledModel struct {
	ID      string
	Name    string
	Enabled bool
}

// —— wire types ——

type wireMeter struct {
	StartsAt        *string `json:"startsAt"`
	ResetsAt        string  `json:"resetsAt"`
	LimitMicroCents string  `json:"limitMicroCents"`
	UsedMicroCents  string  `json:"usedMicroCents"`
}

type wireGoStatus struct {
	SubscriberUserID string `json:"subscriberUserId"`
	Product          string `json:"product"`
	RenewalProduct   string `json:"renewalProduct"`
	Access           *struct {
		StartsAt    string `json:"startsAt"`
		EndsAt      string `json:"endsAt"`
		CancelAtEnd bool   `json:"cancelAtPeriodEnd"`
		Meters      struct {
			FiveHour wireMeter `json:"fiveHour"`
			Week     wireMeter `json:"week"`
			Month    wireMeter `json:"month"`
		} `json:"meters"`
	} `json:"access"`
}

type wireUsageRow struct {
	Model                string `json:"model"`
	Provider             string `json:"provider"`
	TotalRequests        string `json:"totalRequests"`
	TotalInputTokens     string `json:"totalInputTokens"`
	TotalOutputTokens    string `json:"totalOutputTokens"`
	TotalCacheReadTokens string `json:"totalCacheReadTokens"`
	TotalCacheWrite5m    string `json:"totalCacheWrite5mTokens"`
	TotalCacheWrite1h    string `json:"totalCacheWrite1hTokens"`
	TotalCostMicroCents  string `json:"totalCostMicroCents"`
}

type wireUsageResp struct {
	Items    []wireUsageRow `json:"items"`
	PageInfo struct {
		Page      int `json:"page"`
		PageSize  int `json:"pageSize"`
		Total     int `json:"total"`
		PageCount int `json:"pageCount"`
	} `json:"pageInfo"`
}

type wireSubscriptionModels struct {
	Models []struct {
		ID      string `json:"id"`
		Name    string `json:"name"`
		Enabled bool   `json:"enabled"`
	} `json:"models"`
}

// parseMicroCents reads the API's string-encoded integers.
func parseMicroCents(s string) (MicroCents, error) {
	if s == "" || s == "-" {
		return 0, nil
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse %q as microCents: %w", s, err)
	}
	return MicroCents(v), nil
}

func parseCount(s string) int64 {
	if s == "" || s == "-" {
		return 0
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0
	}
	return v
}

func parseTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t.UTC()
}

func (w wireMeter) toMeter() (Meter, error) {
	lim, err := parseMicroCents(w.LimitMicroCents)
	if err != nil {
		return Meter{}, err
	}
	used, err := parseMicroCents(w.UsedMicroCents)
	if err != nil {
		return Meter{}, err
	}
	return Meter{
		StartsAt:        parseTime(deref(w.StartsAt)),
		ResetsAt:        parseTime(w.ResetsAt),
		LimitMicroCents: lim,
		UsedMicroCents:  used,
	}, nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func (w wireGoStatus) toGoStatus() (GoStatus, error) {
	out := GoStatus{
		SubscriberUserID: w.SubscriberUserID,
		Product:          w.Product,
		RenewalProduct:   w.RenewalProduct,
	}
	if w.Access == nil {
		return out, nil
	}
	fh, err := w.Access.Meters.FiveHour.toMeter()
	if err != nil {
		return out, err
	}
	wk, err := w.Access.Meters.Week.toMeter()
	if err != nil {
		return out, err
	}
	mo, err := w.Access.Meters.Month.toMeter()
	if err != nil {
		return out, err
	}
	out.Access = &GoAccess{
		StartsAt:    parseTime(w.Access.StartsAt),
		EndsAt:      parseTime(w.Access.EndsAt),
		CancelAtEnd: w.Access.CancelAtEnd,
		Meters:      Meters{FiveHour: fh, Week: wk, Month: mo},
	}
	return out, nil
}

func (r wireUsageRow) toModelUsage() (ModelUsage, error) {
	cost, err := parseMicroCents(r.TotalCostMicroCents)
	if err != nil {
		return ModelUsage{}, err
	}
	return ModelUsage{
		Model:          r.Model,
		Provider:       r.Provider,
		Requests:       parseCount(r.TotalRequests),
		InputTokens:    parseCount(r.TotalInputTokens),
		OutputTokens:   parseCount(r.TotalOutputTokens),
		CacheRead:      parseCount(r.TotalCacheReadTokens),
		CacheWrite5m:   parseCount(r.TotalCacheWrite5m),
		CacheWrite1h:   parseCount(r.TotalCacheWrite1h),
		CostMicroCents: cost,
	}, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// CatalogModel is a model's static pricing and limits from /api/v1/models.
type CatalogModel struct {
	ID           string
	Name         string
	ContextLimit int64
	InputLimit   int64
	OutputLimit  int64
	// Prices are USD per 1M tokens.
	InputPerMTok      float64
	OutputPerMTok     float64
	CacheReadPerMTok  float64
	CacheWritePerMTok float64
	// CacheWriteTiers holds per-TTL rates when the model prices them apart.
	CacheWriteTiers map[string]float64
	// HasContextOverride reports a higher price above a context threshold.
	HasContextOverride bool
	OverrideAtContext  int64
	OverrideInputRate  float64
	// HasTimeMultiplier reports that pricing rises for a window of time.
	HasTimeMultiplier bool
	// MultiplierFactor is the advertised price factor (e.g. 2 for double).
	MultiplierFactor float64
	// MultiplierDays are the affected UTC weekdays, when the rule is
	// weekday-scoped.
	MultiplierDays []string
	// MultiplierFromDate is the start date when the rule is date-scoped
	// instead of daily.
	MultiplierFromDate string
}

// Free reports whether the model costs nothing.
func (c CatalogModel) Free() bool {
	return c.InputPerMTok == 0 && c.OutputPerMTok == 0 && c.CacheReadPerMTok == 0
}

// unmarshalLenient is used by tests and callers that want a readable error.
func unmarshalLenient(b []byte, v any) error {
	if err := json.Unmarshal(b, v); err != nil {
		return fmt.Errorf("decode response: %w (body: %s)", err, truncate(string(b), 300))
	}
	return nil
}
