package console

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cydnirn/opencode-limits/internal/config"
)

// userAgent identifies this tool to the Console API.
const userAgent = "opencode-limits/0.1"

const (
	// usagePageSize is the per-page row count requested from the usage endpoint.
	usagePageSize = 100
	// usageMaxPages bounds pagination so a bad pageCount cannot spin forever.
	usageMaxPages = 50
)

// Client talks to the OpenCode Console API.
//
// Requests carry the bearer access token and x-org-id header the API expects.
// A 401 triggers exactly one refresh-and-retry, and the rotated credential is
// persisted before the retry so a crash cannot leave a spent refresh token on
// disk.
type Client struct {
	HTTP   *http.Client
	Store  *Store
	Server string

	mu   sync.Mutex
	cred Credential
}

// NewClient builds a client with sensible defaults.
func NewClient(store *Store, server string) *Client {
	return &Client{
		HTTP:   &http.Client{Timeout: 30 * time.Second},
		Store:  store,
		Server: server,
	}
}

// Credential returns the currently loaded credential.
func (c *Client) Credential() Credential {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cred
}

// serverURL resolves the base origin, preferring the loaded credential's server.
func (c *Client) serverURL() string {
	c.mu.Lock()
	s := c.Server
	if s == "" {
		s = c.cred.Server
	}
	c.mu.Unlock()
	if s == "" {
		s = config.DefaultServer
	}
	return trimSlash(s)
}

// Prepare loads the credential and refreshes it if expired.
//
// It returns ErrNoCredential when the file is absent, which the caller turns
// into a device login.
func (c *Client) Prepare() error {
	cred, err := c.Store.Load()
	if err != nil {
		return err
	}
	if cred.Expired(time.Now()) {
		return c.refreshLocked()
	}
	c.mu.Lock()
	c.cred = cred
	c.mu.Unlock()
	return nil
}

// refreshLocked refreshes using the credential currently on disk and persists
// the rotated result immediately.
//
// Refresh tokens rotate, so if two processes race, the loser gets invalid_grant.
// That does NOT mean the session is dead — the winner has already written a
// usable token to the shared file. So on invalid_grant this reloads the file and
// adopts a fresher credential if one is there, returning ErrConcurrentRefresh
// only when the file still holds the token that just failed. Treating a lost
// race as a dead session would log the user out and, on the --login path,
// overwrite the winner's working credential.
func (c *Client) refreshLocked() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	cur := c.cred
	if cur.Refresh == "" {
		loaded, err := c.Store.Load()
		if err != nil {
			return err
		}
		cur = loaded
		if !cur.Expired(time.Now()) {
			c.cred = cur
			return nil
		}
	}

	r := Refresher{HTTP: c.HTTP, Server: c.Server}
	fresh, err := r.Refresh(cur)
	if err != nil {
		var ae *AuthError
		if errors.As(err, &ae) && ae.IsInvalidGrant() {
			// Another process may have rotated the token underneath us.
			if loaded, lerr := c.Store.Load(); lerr == nil {
				if loaded.Access != "" && loaded.Access != cur.Access {
					c.cred = loaded
					return nil
				}
			}
			return fmt.Errorf("%w: %w", ErrConcurrentRefresh, ae)
		}
		return err
	}
	// Persist before any use: the old refresh token is now dead. There is no
	// cross-process lock here, so a concurrent refresher can still lose the race
	// and receive invalid_grant; that case is recovered below and by the caller.
	// What this does NOT do is detect a rotation that happened between our read
	// and our write - only invalid_grant reveals that, so there is no comparison
	// to perform on the success path.
	if err := c.Store.Save(fresh); err != nil {
		return err
	}
	c.cred = fresh
	return nil
}

// do performs a GET with auth, refreshing once on 401.
func (c *Client) do(path string, query url.Values, out any) error {
	body, err := c.get(path, query)
	if err != nil {
		ae, ok := err.(*AuthError)
		if ok && ae.Status == http.StatusUnauthorized {
			if rerr := c.refreshLocked(); rerr != nil {
				return rerr
			}
			body, err = c.get(path, query)
			if err != nil {
				return err
			}
		} else {
			return err
		}
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("decode %s: %w (body: %s)", path, err, truncate(string(body), 300))
	}
	return nil
}

// get performs a single authenticated GET and returns the body.
func (c *Client) get(path string, query url.Values) ([]byte, error) {
	c.mu.Lock()
	cred := c.cred
	c.mu.Unlock()

	u := c.serverURL() + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("build request %s: %w", path, err)
	}
	req.Header.Set("Authorization", "Bearer "+cred.Access)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", userAgent)
	if cred.OrgID != "" {
		req.Header.Set("x-org-id", cred.OrgID)
	}

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", path, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, &AuthError{Status: resp.StatusCode, Description: truncate(string(body), 200)}
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: unexpected HTTP %d: %s", path, resp.StatusCode, truncate(string(body), 200))
	}
	return body, nil
}

// GoStatus fetches the plan meters.
func (c *Client) GoStatus() (GoStatus, error) {
	var w wireGoStatus
	if err := c.do("/api/go/status", nil, &w); err != nil {
		return GoStatus{}, err
	}
	return w.toGoStatus()
}

// SubscriptionModels lists the models the plan enables.
func (c *Client) SubscriptionModels() ([]EnabledModel, error) {
	var w wireSubscriptionModels
	if err := c.do("/api/go/subscription/models", nil, &w); err != nil {
		return nil, err
	}
	out := make([]EnabledModel, 0, len(w.Models))
	for _, m := range w.Models {
		out = append(out, EnabledModel{ID: m.ID, Name: m.Name, Enabled: m.Enabled})
	}
	return out, nil
}

// UsageSince fetches per-model usage since an instant.
//
// since must be a full RFC3339 timestamp: the API rejects bare durations and
// view/range parameters with HTTP 400.
func (c *Client) UsageSince(since time.Time) ([]ModelUsage, error) {
	q := url.Values{}
	if !since.IsZero() {
		q.Set("since", since.UTC().Format(time.RFC3339))
	}
	q.Set("pageSize", strconv.Itoa(usagePageSize))

	// Follow pageInfo.pageCount rather than trusting a single page: a workspace
	// with many models would otherwise be silently truncated, understating spend
	// and overstating every model's remaining budget.
	out := make([]ModelUsage, 0, usagePageSize)
	for page := 1; page <= usageMaxPages; page++ {
		q.Set("page", strconv.Itoa(page))
		var w wireUsageResp
		if err := c.do("/api/usage/models", q, &w); err != nil {
			return nil, err
		}
		for _, r := range w.Items {
			mu, err := r.toModelUsage()
			if err != nil {
				return nil, fmt.Errorf("model %q: %w", r.Model, err)
			}
			out = append(out, mu)
		}
		if w.PageInfo.PageCount <= 0 || page >= w.PageInfo.PageCount || len(w.Items) == 0 {
			break
		}
	}
	return out, nil
}

// wireMultiplier is one price-multiplier rule.
//
// `factor` arrives in two shapes in the live catalog: a bare number
// (`"factor": 2`) on time-based rules, and an object keyed by modality
// (`"factor": {"text": 2}`) on service-tier rules. Decoding it as a plain
// float64 makes the whole catalog fail with "cannot unmarshal object into ...
// of type float64", which took the Pricing screen down.
type wireMultiplier struct {
	Factor multiFactor `json:"factor"`
	When   struct {
		UTCDays struct {
			In []string `json:"in"`
		} `json:"utc_days"`
		Date struct {
			Gte string `json:"gte"`
		} `json:"date"`
		ServiceTier struct {
			Equals string `json:"equals"`
		} `json:"service_tier"`
	} `json:"when"`
}

// multiFactor accepts either a number or a per-modality object.
type multiFactor float64

// UnmarshalJSON reads a number, or an object and takes its "text" factor (the
// only modality this tool prices), or null.
func (m *multiFactor) UnmarshalJSON(b []byte) error {
	raw := bytes.TrimSpace(b)
	if len(raw) == 0 || string(raw) == "null" {
		*m = 0
		return nil
	}
	if raw[0] == '{' {
		var byModality map[string]float64
		if err := json.Unmarshal(raw, &byModality); err != nil {
			return fmt.Errorf("multiplier factor object: %w", err)
		}
		// Text is the modality being priced; prefer it, else take any value so a
		// future modality still surfaces rather than being dropped.
		if v, ok := byModality["text"]; ok {
			*m = multiFactor(v)
			return nil
		}
		for _, v := range byModality {
			*m = multiFactor(v)
			return nil
		}
		*m = 0
		return nil
	}
	var v float64
	if err := json.Unmarshal(raw, &v); err != nil {
		return fmt.Errorf("multiplier factor: %w", err)
	}
	*m = multiFactor(v)
	return nil
}

// flexFloat decodes a JSON number or a numeric string.
//
// The Console's model catalog is inconsistent across fields and across models:
// prices arrive as JSON numbers (all 77 entries in the live catalog) while other
// counters arrive as strings. Decoding into a plain float64 fails on the string
// form and decoding into a string fails on the numeric form, so every price
// field uses this type instead.
type flexFloat float64

// UnmarshalJSON accepts a number, a numeric string, null, or "-".
func (f *flexFloat) UnmarshalJSON(b []byte) error {
	s := strings.TrimSpace(string(b))
	if s == "null" || s == `""` || s == `"-"` || s == "0" {
		*f = 0
		return nil
	}
	if unquoted, ok := strings.CutPrefix(s, `"`); ok {
		s = strings.TrimSuffix(unquoted, `"`)
	}
	if s == "" || s == "-" {
		*f = 0
		return nil
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return fmt.Errorf("price value %s is neither a number nor a numeric string: %w", string(b), err)
	}
	*f = flexFloat(v)
	return nil
}

// wirePrice is one price entry in the catalog.
type wirePrice struct {
	Price     flexFloat      `json:"price"`
	Cache     *wireCache     `json:"cache"`
	Overrides []wireOverride `json:"overrides"`
}

type wireCache struct {
	Read  flexFloat       `json:"read"`
	Write json.RawMessage `json:"write"`
}

type wireOverride struct {
	When struct {
		Context struct {
			Gt int64 `json:"gt"`
		} `json:"context"`
	} `json:"when"`
	Price flexFloat `json:"price"`
}

type wireModel struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Limit struct {
		Context int64 `json:"context"`
		Input   int64 `json:"input"`
		Output  int64 `json:"output"`
	} `json:"limit"`
	Cost struct {
		Text struct {
			Input  wirePrice `json:"input"`
			Output wirePrice `json:"output"`
		} `json:"text"`
		// Multipliers sits on cost, not cost.text, and applies to the whole
		// model's pricing.
		Multipliers []wireMultiplier `json:"multipliers"`
	} `json:"cost"`
}

// Catalog fetches static model pricing.
func (c *Client) Catalog() (map[string]CatalogModel, error) {
	// Decode into raw messages first so a single model with an unexpected shape
	// cannot fail the whole catalog. That has happened twice: prices arriving as
	// numbers instead of strings, and multiplier factors arriving as objects
	// instead of numbers. Both took the entire Pricing screen down over one field.
	var envelope map[string]struct {
		Models map[string]json.RawMessage `json:"models"`
	}
	if err := c.do("/api/v1/models", nil, &envelope); err != nil {
		return nil, err
	}

	out := map[string]CatalogModel{}
	var skipped []string
	for _, prov := range envelope {
		for id, raw := range prov.Models {
			var m wireModel
			if err := json.Unmarshal(raw, &m); err != nil {
				// Record which model and why, then carry on: the rest of the
				// catalog is still useful.
				skipped = append(skipped, fmt.Sprintf("%s: %v", id, err))
				continue
			}
			if id == "" {
				id = m.ID
			}
			cm := CatalogModel{
				ID:           id,
				Name:         m.Name,
				ContextLimit: m.Limit.Context,
				InputLimit:   m.Limit.Input,
				OutputLimit:  m.Limit.Output,
			}
			cm.InputPerMTok = float64(m.Cost.Text.Input.Price)
			cm.OutputPerMTok = float64(m.Cost.Text.Output.Price)
			if m.Cost.Text.Input.Cache != nil {
				cm.CacheReadPerMTok = float64(m.Cost.Text.Input.Cache.Read)
				tiers := map[string]float64{}
				var scalar float64
				if err := json.Unmarshal(m.Cost.Text.Input.Cache.Write, &scalar); err == nil {
					cm.CacheWritePerMTok = scalar
				} else {
					_ = json.Unmarshal(m.Cost.Text.Input.Cache.Write, &tiers)
					cm.CacheWriteTiers = tiers
					// Report the cheapest tier: that is what a session normally pays.
					best := -1.0
					for _, v := range tiers {
						if best < 0 || v < best {
							best = v
						}
					}
					if best > 0 {
						cm.CacheWritePerMTok = best
					}
				}
			}
			for _, ov := range m.Cost.Text.Input.Overrides {
				if ov.When.Context.Gt > 0 {
					cm.HasContextOverride = true
					cm.OverrideAtContext = ov.When.Context.Gt
					cm.OverrideInputRate = float64(ov.Price)
					break
				}
			}
			if len(m.Cost.Multipliers) > 0 {
				// Report the largest advertised factor rather than assuming 2.
				cm.HasTimeMultiplier = true
				best := 0.0
				for _, mul := range m.Cost.Multipliers {
					f := float64(mul.Factor)
					if f > best {
						best = f
						cm.MultiplierFactor = f
						cm.MultiplierDays = mul.When.UTCDays.In
						cm.MultiplierFromDate = mul.When.Date.Gte
						cm.MultiplierServiceTier = mul.When.ServiceTier.Equals
					}
				}
				cm.MultiplierFactor = best
			}
			out[id] = cm
		}
	}
	if len(skipped) > 0 {
		// Partial data is reported, never hidden: the Pricing screen says how many
		// models it could not read, so a silent gap cannot be mistaken for "this
		// plan has fewer models".
		return out, &CatalogDecodeError{Skipped: skipped}
	}
	return out, nil
}

// CatalogDecodeError reports models the catalog could not decode while the rest
// succeeded. Callers should still use the returned map.
type CatalogDecodeError struct {
	Skipped []string
}

func (e *CatalogDecodeError) Error() string {
	return fmt.Sprintf("catalog: %d model(s) could not be decoded: %s",
		len(e.Skipped), truncate(strings.Join(e.Skipped, "; "), 300))
}

// ActivityGo is GET /api/activity/go — daily totals plus request counts.
type ActivityGo struct {
	StartsAt time.Time
	Daily    []ActivityDay
	Models   []ActivityModel
}

// ActivityDay is one day's totals.
type ActivityDay struct {
	Date     string
	Cost     MicroCents
	Tokens   int64
	Requests int64
}

// ActivityModel is a per-model request count over the activity range.
type ActivityModel struct {
	Provider string
	Model    string
	Requests int64
}

// Activity fetches daily activity.
func (c *Client) Activity() (ActivityGo, error) {
	var w struct {
		StartsAt string `json:"startsAt"`
		Daily    []struct {
			Date          string `json:"date"`
			TotalCost     string `json:"totalCostMicroCents"`
			TotalTokens   string `json:"totalTokens"`
			TotalRequests string `json:"totalRequests"`
		} `json:"daily"`
		Models []struct {
			Provider      string `json:"provider"`
			Model         string `json:"model"`
			TotalRequests string `json:"totalRequests"`
		} `json:"models"`
	}
	if err := c.do("/api/activity/go", nil, &w); err != nil {
		return ActivityGo{}, err
	}
	out := ActivityGo{StartsAt: parseTime(w.StartsAt)}
	for _, d := range w.Daily {
		cost, _ := parseMicroCents(d.TotalCost)
		out.Daily = append(out.Daily, ActivityDay{
			Date:     d.Date,
			Cost:     cost,
			Tokens:   parseCount(d.TotalTokens),
			Requests: parseCount(d.TotalRequests),
		})
	}
	for _, m := range w.Models {
		out.Models = append(out.Models, ActivityModel{
			Provider: m.Provider,
			Model:    m.Model,
			Requests: parseCount(m.TotalRequests),
		})
	}
	return out, nil
}

// BillingStatus is GET /api/billing/drawer/status — extra-usage context.
type BillingStatus struct {
	BillingMode         string
	BalanceMicroCents   MicroCents
	AvailableMicroCents MicroCents
	Funded              bool
}

// Billing fetches balance and billing mode.
func (c *Client) Billing() (BillingStatus, error) {
	var w struct {
		Billing struct {
			BillingMode string `json:"billingMode"`
			Balance     string `json:"balanceMicroCents"`
			Available   string `json:"availableMicroCents"`
		} `json:"billing"`
		Funded bool `json:"funded"`
	}
	if err := c.do("/api/billing/drawer/status", nil, &w); err != nil {
		return BillingStatus{}, err
	}
	bal, _ := parseMicroCents(w.Billing.Balance)
	avail, _ := parseMicroCents(w.Billing.Available)
	return BillingStatus{
		BillingMode:         w.Billing.BillingMode,
		BalanceMicroCents:   bal,
		AvailableMicroCents: avail,
		Funded:              w.Funded,
	}, nil
}
