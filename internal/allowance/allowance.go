// Package allowance holds the plan's published per-model monthly limits.
//
// OpenCode does not expose these over its API. The Console's model catalog
// (/api/v1/models) and the models.dev feed both omit the `go` block entirely;
// only the public web bundle carries it, as `go.monthly_usage` per model, keyed
// by plan. Without these numbers the tool could only show spend, and the operator
// had no way to know how much each model was actually allowed.
//
// These are per-model caps, not a shared budget. Each model has its own monthly
// ceiling, and exhausting one says nothing about the others: using $15 of GPT-6
// Luna does not reduce what GLM-5.3 may spend. That is why the tool reports
// "remaining" per model against these caps and never sums them into one pool.
//
// Values in Table were read from the Console's shipped bundle on 2026-11-18 and
// cross-checked against the published table at https://opencode.ai/console/go.
// Fetch refreshes them from the same bundle when the network allows, so a stale
// embedded copy degrades to "older numbers" rather than "no numbers".
package allowance

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Limit is one model's monthly allowance under each plan. A nil pointer means the
// model is unlimited on that plan.
type Limit struct {
	Monthly map[string]float64
}

// MonthlyFor returns the monthly cap for a plan, and whether it is unlimited.
func (l Limit) MonthlyFor(plan string) (usd float64, unlimited bool, ok bool) {
	if len(l.Monthly) == 0 {
		return 0, false, false
	}
	v, has := l.Monthly[plan]
	if !has {
		return 0, false, false
	}
	// A null monthly_usage upstream is recorded as +Inf, meaning no cap.
	if v == 0 {
		return 0, true, true
	}
	return v, false, true
}

// Table maps model ID to its per-plan limits.
type Table map[string]Limit

// Lookup returns a model's limits.
func (t Table) Lookup(model string) (Limit, bool) {
	l, ok := t[model]
	return l, ok
}

// ModelIDs returns the known model IDs, sorted.
func (t Table) ModelIDs() []string {
	ids := make([]string, 0, len(t))
	for id := range t {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// Source names where a table came from, for display.
type Source string

const (
	SourceEmbedded Source = "embedded"
	SourceLive     Source = "live"
)

// embeddedJSON is the table as captured from the Console bundle. Keep it tidy and
// sorted; gofmt will not reflow it.
const embeddedJSON = `{
 "beam-free":               {"go":0,"go-plus":0,"go-teams":0},
 "claude-haiku-5-5":        {"go":15,"go-plus":60,"go-teams":20},
 "deepseek-v4-pro":         {"go":15,"go-plus":60,"go-teams":20},
 "deepseek-v4.1-flash":     {"go":100,"go-plus":200,"go-teams":105},
 "gemini-4-argon":          {"go":30,"go-plus":120,"go-teams":35},
 "glm-5.2":                 {"go":60,"go-plus":180,"go-teams":65},
 "glm-5.3":                 {"go":500,"go-plus":1000,"go-teams":505},
 "glm-5.3-flash":           {"go":60,"go-plus":180,"go-teams":65},
 "gpt-5.6-luna":            {"go":15,"go-plus":60,"go-teams":20},
 "gpt-6-luna":              {"go":15,"go-plus":60,"go-teams":20},
 "grok-4.7":                {"go":15,"go-plus":60,"go-teams":20},
 "hy3":                     {"go":60,"go-plus":240,"go-teams":65},
 "hy4":                     {"go":60,"go-plus":240,"go-teams":65},
 "hy4-flash-free":          {"go":0,"go-plus":0,"go-teams":0},
 "hy4-preview":             {"go":30,"go-plus":120,"go-teams":35},
 "iceman-contributor-free": {"go":0,"go-plus":0,"go-teams":0},
 "kimi-k2.7-code":          {"go":60,"go-plus":180,"go-teams":65},
 "kimi-k3":                 {"go":15,"go-plus":60,"go-teams":20},
 "kimi-k3.1-pro":           {"go":15,"go-plus":60,"go-teams":20},
 "longcat-2.0":             {"go":60,"go-plus":240,"go-teams":65},
 "longcat-2.5-free":        {"go":0,"go-plus":0,"go-teams":0},
 "mimo-v2.6-flash":         {"go":60,"go-plus":120,"go-teams":65},
 "mimo-v2.6-pro":           {"go":15,"go-plus":60,"go-teams":20},
 "minimax-m3":              {"go":60,"go-plus":180,"go-teams":65},
 "minimax-m3.1-flash":      {"go":20,"go-plus":80,"go-teams":25},
 "muse-spark-1.2-contributor": {"go":60,"go-plus":120,"go-teams":65},
 "muse-spark-1.3-contributor": {"go":60,"go-plus":120,"go-teams":65},
 "qwen3.7-plus":            {"go":60,"go-plus":180,"go-teams":65},
 "qwen3.8-flash":           {"go":30,"go-plus":90,"go-teams":35},
 "qwen3.8-max":             {"go":15,"go-plus":60,"go-teams":20},
 "qwen4-max":               {"go":15,"go-plus":60,"go-teams":20},
 "step-5-preview":          {"go":15,"go-plus":60,"go-teams":20}
}`

// Embedded returns the table shipped with the binary.
func Embedded() Table {
	t, err := parseTable([]byte(embeddedJSON))
	if err != nil {
		// The literal above is validated by a test, so this is unreachable in a
		// passing build; return an empty table rather than panicking at runtime.
		return Table{}
	}
	return t
}

func parseTable(b []byte) (Table, error) {
	var raw map[string]map[string]float64
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, fmt.Errorf("parse allowance table: %w", err)
	}
	out := make(Table, len(raw))
	for id, plans := range raw {
		l := Limit{Monthly: map[string]float64{}}
		for plan, v := range plans {
			l.Monthly[plan] = v
		}
		out[id] = l
	}
	return out, nil
}

// The page that carries go.monthly_usage, and the asset directory it loads from.
// These are variables rather than constants so tests can serve a local copy; the
// asset hash changes between deploys, which is why the page is read first to
// discover the current script.
var (
	consoleGoPage = "https://opencode.ai/console/go"
	bundleBase    = "https://opencode.ai/console/_build/assets/"
)

// assetRE finds the go page's script asset.
var assetRE = regexp.MustCompile(`/console/_build/assets/(go-[A-Za-z0-9_-]+\.js)`)

// monthlyRE captures `monthly_usage:{go:15,"go-plus":60,...}`.
// monthlyRE matches both `monthly_usage:{...}` and the bare `monthly_usage:null`
// form, which means the model has no cap.
var monthlyRE = regexp.MustCompile(`monthly_usage:(?:\{([^}]*)\}|null)`)

// idRE captures the model ID that precedes a go block.
var idRE = regexp.MustCompile("id:`([A-Za-z0-9.\\-]+)`")

// numberRE captures `go:500` or `"go-plus":1e3`.
var numberRE = regexp.MustCompile(`"?([a-z\-]+)"?:([0-9.]+(?:e[0-9]+)?|null|-?Infinity)`)

// Fetch reads the live table from the Console's public bundle.
//
// It is best-effort: any failure returns an error and the caller keeps the
// embedded table. No credential is required because the page and its bundle are
// public.
func Fetch(ctx context.Context, hc *http.Client) (Table, error) {
	if hc == nil {
		hc = &http.Client{Timeout: 20 * time.Second}
	}
	get := func(u string) ([]byte, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", "opencode-limits")
		resp, err := hc.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("GET %s: HTTP %d", u, resp.StatusCode)
		}
		return io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	}

	page, err := get(consoleGoPage)
	if err != nil {
		return nil, fmt.Errorf("read console go page: %w", err)
	}
	m := assetRE.FindSubmatch(page)
	if m == nil {
		return nil, fmt.Errorf("no go asset found on %s", consoleGoPage)
	}
	js, err := get(bundleBase + string(m[1]))
	if err != nil {
		return nil, fmt.Errorf("read go bundle: %w", err)
	}
	return ParseBundle(js)
}

// ParseBundle extracts the allowance table from the go page's JavaScript.
func ParseBundle(js []byte) (Table, error) {
	text := string(js)
	// Pair each model id with the first monthly_usage block that follows it.
	ids := idRE.FindAllStringSubmatchIndex(text, -1)
	months := monthlyRE.FindAllStringSubmatchIndex(text, -1)
	if len(ids) == 0 || len(months) == 0 {
		return nil, fmt.Errorf("bundle has no monthly_usage blocks (ids=%d)", len(ids))
	}

	out := Table{}
	mi := 0
	for _, idm := range ids {
		id := text[idm[2]:idm[3]]
		// Advance to the first monthly block that starts after this id.
		for mi < len(months) && months[mi][0] < idm[1] {
			mi++
		}
		if mi >= len(months) {
			break
		}
		var body string
		if months[mi][2] >= 0 {
			body = text[months[mi][2]:months[mi][3]]
		}
		limit, ok := parseMonthly(body)
		if !ok {
			// `monthly_usage:null` means unlimited, not absent. Record it so the
			// pairing does not attach the next model's block to this id.
			out[id] = Limit{Monthly: map[string]float64{"go": 0, "go-plus": 0, "go-teams": 0}}
			continue
		}
		out[id] = limit
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no models parsed from bundle")
	}
	return out, nil
}

// parseMonthly decodes the fields of one monthly_usage block.
func parseMonthly(body string) (Limit, bool) {
	l := Limit{Monthly: map[string]float64{}}
	for _, m := range numberRE.FindAllStringSubmatch(body, -1) {
		plan, val := m[1], m[2]
		switch val {
		case "null", "Infinity":
			// No cap: recorded as 0, which MonthlyFor reports as unlimited.
			l.Monthly[plan] = 0
		case "-Infinity":
			continue
		default:
			v, err := strconv.ParseFloat(val, 64)
			if err != nil {
				continue
			}
			l.Monthly[plan] = v
		}
	}
	return l, len(l.Monthly) > 0
}

// Resolver caches the table and refreshes it at most once per TTL.
type Resolver struct {
	HTTP *http.Client
	TTL  time.Duration
	// Fetch overrides the network call, for tests.
	Fetch func(context.Context, *http.Client) (Table, error)
	// Now is injectable for tests.
	Now func() time.Time

	mu      sync.Mutex
	table   Table
	source  Source
	fetched time.Time
	err     error
}

// Get returns the best available table and its provenance.
func (r *Resolver) Get(ctx context.Context) (Table, Source) {
	r.mu.Lock()
	defer r.mu.Unlock()

	now := time.Now()
	if r.Now != nil {
		now = r.Now()
	}
	ttl := r.TTL
	if ttl == 0 {
		ttl = 6 * time.Hour
	}
	if r.table != nil && now.Sub(r.fetched) < ttl {
		return r.table, r.source
	}

	// First call in this process uses the embedded copy immediately so the UI
	// never blocks on the network, then tries to refresh.
	if r.table == nil {
		r.table = Embedded()
		r.source = SourceEmbedded
		r.fetched = now
	}

	fetch := r.Fetch
	if fetch == nil {
		fetch = Fetch
	}
	live, err := fetch(ctx, r.HTTP)
	if err != nil {
		r.err = err
		return r.table, r.source
	}
	r.table = live
	r.source = SourceLive
	r.fetched = now
	r.err = nil
	return r.table, r.source
}

// LastError reports the most recent refresh failure, if any.
func (r *Resolver) LastError() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.err
}

// Compare reports differences between two tables as human-readable lines, used by
// tests and by --refresh-allowances to show what changed upstream.
func Compare(a, b Table) []string {
	var diffs []string
	for id, la := range a {
		lb, ok := b[id]
		if !ok {
			diffs = append(diffs, fmt.Sprintf("%s: missing from new table", id))
			continue
		}
		plans := map[string]bool{}
		for p := range la.Monthly {
			plans[p] = true
		}
		for p := range lb.Monthly {
			plans[p] = true
		}
		names := make([]string, 0, len(plans))
		for p := range plans {
			names = append(names, p)
		}
		sort.Strings(names)
		for _, p := range names {
			if la.Monthly[p] != lb.Monthly[p] {
				diffs = append(diffs, fmt.Sprintf("%s/%s: %g -> %g", id, p, la.Monthly[p], lb.Monthly[p]))
			}
		}
	}
	for id := range b {
		if _, ok := a[id]; !ok {
			diffs = append(diffs, id+": new model")
		}
	}
	sort.Strings(diffs)
	return diffs
}

// String renders a table entry for display, e.g. "$15" or "unlimited".
func (l Limit) String(plan string) string {
	usd, unlimited, ok := l.MonthlyFor(plan)
	if !ok {
		return "--"
	}
	if unlimited {
		return "unlimited"
	}
	return "$" + trimZero(usd)
}

func trimZero(v float64) string {
	s := strconv.FormatFloat(v, 'f', -1, 64)
	return strings.TrimSuffix(s, ".0")
}
