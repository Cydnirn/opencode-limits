package console

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// fixtures reproduce the real payload shapes captured from the live Console,
// including string-encoded integers, so parsing is tested against reality.
const goStatusJSON = `{
  "subscriberUserId":"user_01M7G87PCAMEMG1WG5RQHX69DS",
  "product":"go","renewalProduct":"go","useBalance":false,
  "access":{
    "startsAt":"2026-11-16T18:09:18.000Z",
    "endsAt":"2026-12-16T18:09:18.000Z",
    "cancelAtPeriodEnd":false,
    "meters":{
      "fiveHour":{"startsAt":"2026-11-18T06:59:21.000Z","resetsAt":"2026-11-18T11:59:21.000Z","limitMicroCents":"1200000000","usedMicroCents":"101480253"},
      "week":{"startsAt":"2026-11-16T00:00:00.000Z","resetsAt":"2026-11-23T00:00:00.000Z","limitMicroCents":"3000000000","usedMicroCents":"1666042828"},
      "month":{"resetsAt":"2026-12-16T18:09:18.000Z","limitMicroCents":"6000000000","usedMicroCents":"1666042828"}
    }
  }
}`

const usageModelsJSON = `{"items":[
 {"model":"gpt-6-luna","provider":"opencode-go","subscription":false,
  "totalRequests":"831","totalInputTokens":"65050","totalOutputTokens":"589013",
  "totalCacheReadTokens":"144102919","totalCacheWrite5mTokens":"3622920",
  "totalCacheWrite1hTokens":"0","totalCostMicroCents":"307223321"},
 {"model":"deepseek-v4.1-flash","provider":"opencode-go","subscription":false,
  "totalRequests":"1519","totalInputTokens":"2999201","totalOutputTokens":"2319634",
  "totalCacheReadTokens":"131413248","totalCacheWrite5mTokens":"0",
  "totalCacheWrite1hTokens":"0","totalCostMicroCents":"305796761"}
],"pageInfo":{"page":1,"pageSize":10,"total":2,"pageCount":1}}`

const subscriptionModelsJSON = `{"models":[
 {"id":"gpt-6-luna","name":"GPT-6 Luna","enabled":true},
 {"id":"glm-5.3","name":"GLM-5.3","enabled":true}
],"newModelsEnabled":true}`

const catalogJSON = `{"opencode":{"id":"opencode","name":"Personal / OpenCode","models":{
 "gpt-6-luna":{"id":"gpt-6-luna","name":"GPT-6 Luna",
   "limit":{"context":1050000,"input":922000,"output":128000},
   "cost":{"text":{
     "input":{"price":"0.1","cache":{"read":"0.01","write":{"30m":0.125,"12h":0.25}},
              "overrides":[{"when":{"context":{"gt":272000}},"price":0.2}]},
     "output":{"price":"0.5"}}}},
 "glm-5.3":{"id":"glm-5.3","name":"GLM-5.3",
   "limit":{"context":1000000,"output":131072},
   "cost":{"text":{"input":{"price":"1.4","cache":{"read":"0.26"}},"output":{"price":"4.4"}}}},
 "deepseek-v4.1-flash":{"id":"deepseek-v4.1-flash","name":"DeepSeek V4.1 Flash",
   "limit":{"context":1000000,"output":384000},
   "cost":{"text":{"input":{"price":"0.15","cache":{"read":"0.003"}},"output":{"price":"0.6"},
             "multipliers":[{"when":{"utc_days":{"in":["mon"]}},"factor":2}]}}},
 "hy4-flash-free":{"id":"hy4-flash-free","name":"Hy4 flash Free",
   "limit":{"context":1048576,"output":65536},
   "cost":{"text":{"input":{"price":"0","cache":{"read":"0"}},"output":{"price":"0"}}}}
}}}`

const activityJSON = `{"startsAt":"2026-11-16T18:09:18.000Z",
 "daily":[{"date":"2026-11-16","totalCostMicroCents":"128146422","totalTokens":"5271635","totalRequests":"28"},
          {"date":"2026-11-17","totalCostMicroCents":"446014192","totalTokens":"237241356","totalRequests":"1865"}],
 "models":[{"provider":"opencode-go","model":"deepseek-v4.1-flash","totalRequests":"1501"}]}`

const billingJSON = `{"admins":[{"name":"a@b.c","avatarUrl":null}],"hasPaymentMethod":true,
 "billing":{"billingMode":"prepaid","mode":"pay-as-you-go","balanceMicroCents":"0",
 "creditLimitMicroCents":null,"availableMicroCents":"0"},
 "autoRecharge":{"enabled":false},"funded":false,"overdue":false,
 "budgets":{"workspace":null,"personal":null}}`

func writeCredential(t *testing.T, path string, c Credential) {
	t.Helper()
	doc := map[string]any{
		"type":       c.Type,
		"server":     c.Server,
		"access":     c.Access,
		"refresh":    c.Refresh,
		"expires":    c.Expires,
		"accountID":  c.AccountID,
		"email":      c.Email,
		"orgID":      c.OrgID,
		"orgName":    c.OrgName,
		"foreignKey": "keep-me", // must survive a rewrite
	}
	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// newTestServer serves the fixture endpoints and enforces the auth headers.
func newTestServer(t *testing.T, wantOrg string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	serve := func(path, body string) {
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
				http.Error(w, "no bearer", http.StatusUnauthorized)
				return
			}
			if wantOrg != "" && r.Header.Get("x-org-id") != wantOrg {
				http.Error(w, "wrong org", http.StatusForbidden)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, body)
		})
	}
	serve("/api/go/status", goStatusJSON)
	serve("/api/go/subscription/models", subscriptionModelsJSON)
	serve("/api/v1/models", catalogJSON)
	serve("/api/activity/go", activityJSON)
	serve("/api/billing/drawer/status", billingJSON)
	mux.HandleFunc("/api/usage/models", func(w http.ResponseWriter, r *http.Request) {
		// The since parameter must be a full RFC3339 instant.
		if since := r.URL.Query().Get("since"); since != "" {
			if _, err := time.Parse(time.RFC3339, since); err != nil {
				http.Error(w, "invalid since", http.StatusBadRequest)
				return
			}
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, usageModelsJSON)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestClientParsesLiveShapes(t *testing.T) {
	srv := newTestServer(t, "org_test")
	credPath := filepath.Join(t.TempDir(), "opencode-console", "credential.json")
	writeCredential(t, credPath, Credential{
		Type: "oauth", Server: srv.URL, Access: "tok", Refresh: "ref",
		Expires: time.Now().Add(time.Hour).UnixMilli(), OrgID: "org_test",
	})

	c := NewClient(&Store{Path: credPath}, srv.URL)
	if err := c.Prepare(); err != nil {
		t.Fatalf("Prepare: %v", err)
	}

	st, err := c.GoStatus()
	if err != nil {
		t.Fatalf("GoStatus: %v", err)
	}
	if st.Product != "go" {
		t.Errorf("product = %q", st.Product)
	}
	if st.Access == nil {
		t.Fatal("nil access")
	}
	if got := st.Access.Meters.FiveHour.LimitMicroCents.USD(); got != 12 {
		t.Errorf("5h limit = %v USD, want 12", got)
	}
	if got := st.Access.Meters.FiveHour.UsedMicroCents.USD(); got < 1.01 || got > 1.02 {
		t.Errorf("5h used = %v USD, want ~1.0148", got)
	}
	if st.Access.Meters.FiveHour.ResetsAt.IsZero() {
		t.Error("5h resetsAt not parsed")
	}

	models, err := c.SubscriptionModels()
	if err != nil {
		t.Fatalf("SubscriptionModels: %v", err)
	}
	if len(models) != 2 || models[0].ID != "gpt-6-luna" {
		t.Errorf("models = %+v", models)
	}

	usage, err := c.UsageSince(time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("UsageSince: %v", err)
	}
	if len(usage) != 2 {
		t.Fatalf("usage rows = %d", len(usage))
	}
	var luna ModelUsage
	for _, u := range usage {
		if u.Model == "gpt-6-luna" {
			luna = u
		}
	}
	if luna.Requests != 831 || luna.CacheRead != 144102919 {
		t.Errorf("luna parse = %+v", luna)
	}
	if luna.CostMicroCents != MicroCents(307223321) {
		t.Errorf("luna cost = %d", luna.CostMicroCents)
	}

	cat, err := c.Catalog()
	if err != nil {
		t.Fatalf("Catalog: %v", err)
	}
	lunaCat := cat["gpt-6-luna"]
	if !lunaCat.HasContextOverride || lunaCat.OverrideAtContext != 272000 {
		t.Errorf("context override not detected: %+v", lunaCat)
	}
	if lunaCat.CacheWritePerMTok != 0.125 {
		t.Errorf("cache write cheapest tier = %v, want 0.125", lunaCat.CacheWritePerMTok)
	}
	if len(lunaCat.CacheWriteTiers) != 2 {
		t.Errorf("cache write tiers = %+v", lunaCat.CacheWriteTiers)
	}
	if !cat["deepseek-v4.1-flash"].HasTimeMultiplier {
		t.Error("time multiplier not detected")
	}
	if !cat["hy4-flash-free"].Free() {
		t.Error("free model not recognised as free")
	}
	if cat["glm-5.3"].OutputPerMTok != 4.4 {
		t.Errorf("glm output price = %v", cat["glm-5.3"].OutputPerMTok)
	}
	if cat["glm-5.3"].ContextLimit != 1000000 {
		t.Errorf("glm context = %v", cat["glm-5.3"].ContextLimit)
	}

	act, err := c.Activity()
	if err != nil {
		t.Fatalf("Activity: %v", err)
	}
	if len(act.Daily) != 2 || act.Daily[1].Requests != 1865 {
		t.Errorf("activity = %+v", act)
	}

	if _, err := c.Billing(); err != nil {
		t.Fatalf("Billing: %v", err)
	}
}

func TestUsageSinceFormatsTimestamp(t *testing.T) {
	srv := newTestServer(t, "")
	credPath := filepath.Join(t.TempDir(), "credential.json")
	writeCredential(t, credPath, Credential{
		Server: srv.URL, Access: "tok",
		Expires: time.Now().Add(time.Hour).UnixMilli(),
	})
	c := NewClient(&Store{Path: credPath}, srv.URL)
	if err := c.Prepare(); err != nil {
		t.Fatal(err)
	}
	if _, err := c.UsageSince(time.Time{}); err != nil {
		t.Fatalf("zero since should be allowed: %v", err)
	}
	if _, err := c.UsageSince(time.Now().Add(-5 * time.Hour)); err != nil {
		t.Fatalf("valid since failed: %v", err)
	}
}

func TestMissingCredential(t *testing.T) {
	c := NewClient(&Store{Path: filepath.Join(t.TempDir(), "nope.json")}, "http://127.0.0.1:1")
	if err := c.Prepare(); err != ErrNoCredential {
		t.Fatalf("error = %v, want ErrNoCredential", err)
	}
}

// A refresh must persist the rotated token AND keep keys this tool does not
// model, or every other consumer of the file is logged out.
func TestRefreshRotatesAndPreservesForeignKeys(t *testing.T) {
	var refreshCalls atomic.Int64
	mux := http.NewServeMux()
	mux.HandleFunc("/auth/device/token", func(w http.ResponseWriter, r *http.Request) {
		refreshCalls.Add(1)
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["grant_type"] != "refresh_token" || body["client_id"] != ClientID {
			http.Error(w, "bad grant", http.StatusBadRequest)
			return
		}
		if body["refresh_token"] != "old-refresh" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"_tag":"DeviceTokenError","error":"invalid_grant","error_description":"The grant is invalid"}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"access_token":"new-access","refresh_token":"new-refresh","expires_in":3600,"org_id":"org_test"}`)
	})
	mux.HandleFunc("/api/go/status", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer new-access" {
			http.Error(w, "stale token", http.StatusUnauthorized)
			return
		}
		fmt.Fprint(w, goStatusJSON)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	credPath := filepath.Join(t.TempDir(), "opencode-console", "credential.json")
	// Already expired, so Prepare must refresh.
	writeCredential(t, credPath, Credential{
		Type: "oauth", Server: srv.URL, Access: "old-access", Refresh: "old-refresh",
		Expires: time.Now().Add(-time.Hour).UnixMilli(),
		OrgID:   "org_test", AccountID: "user_1", Email: "a@b.c", OrgName: "Personal",
	})

	c := NewClient(&Store{Path: credPath}, srv.URL)
	if err := c.Prepare(); err != nil {
		t.Fatalf("Prepare with expired token: %v", err)
	}
	if refreshCalls.Load() != 1 {
		t.Fatalf("refresh calls = %d, want 1", refreshCalls.Load())
	}

	raw, err := os.ReadFile(credPath)
	if err != nil {
		t.Fatal(err)
	}
	var disk Credential
	if err := json.Unmarshal(raw, &disk); err != nil {
		t.Fatal(err)
	}
	if disk.Access != "new-access" {
		t.Errorf("persisted access = %q, want new-access", disk.Access)
	}
	if disk.Refresh != "new-refresh" {
		t.Errorf("persisted refresh = %q, want new-refresh (rotation lost!)", disk.Refresh)
	}
	if disk.OrgID != "org_test" || disk.Email != "a@b.c" || disk.AccountID != "user_1" || disk.OrgName != "Personal" {
		t.Errorf("credential metadata damaged: %+v", disk)
	}
	if !strings.Contains(string(raw), "keep-me") {
		t.Error("foreign key was stripped from the credential file")
	}

	if _, err := c.GoStatus(); err != nil {
		t.Fatalf("GoStatus after refresh: %v", err)
	}
}

func TestRefreshInvalidGrantSurfaces(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/auth/device/token", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"_tag":"DeviceTokenError","error":"invalid_grant","error_description":"The grant is invalid"}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	credPath := filepath.Join(t.TempDir(), "credential.json")
	writeCredential(t, credPath, Credential{
		Server: srv.URL, Access: "a", Refresh: "spent",
		Expires: time.Now().Add(-time.Hour).UnixMilli(),
	})
	c := NewClient(&Store{Path: credPath}, srv.URL)
	err := c.Prepare()
	if err == nil {
		t.Fatal("expected error")
	}
	ae, ok := err.(*AuthError)
	if !ok {
		t.Fatalf("error type = %T, want *AuthError", err)
	}
	if !ae.IsInvalidGrant() {
		t.Fatalf("expected invalid_grant, got %+v", ae)
	}
}

func TestUnauthorizedTriggersSingleRefreshRetry(t *testing.T) {
	var status401, refreshCalls atomic.Int64
	mux := http.NewServeMux()
	mux.HandleFunc("/auth/device/token", func(w http.ResponseWriter, r *http.Request) {
		refreshCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"access_token":"fresh","refresh_token":"r2","expires_in":3600}`)
	})
	mux.HandleFunc("/api/go/status", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "Bearer fresh" {
			fmt.Fprint(w, goStatusJSON)
			return
		}
		status401.Add(1)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	credPath := filepath.Join(t.TempDir(), "credential.json")
	writeCredential(t, credPath, Credential{
		Server: srv.URL, Access: "stale", Refresh: "r1",
		Expires: time.Now().Add(time.Hour).UnixMilli(),
	})
	c := NewClient(&Store{Path: credPath}, srv.URL)
	if err := c.Prepare(); err != nil {
		t.Fatal(err)
	}
	if _, err := c.GoStatus(); err != nil {
		t.Fatalf("expected retry to succeed: %v", err)
	}
	if status401.Load() != 1 {
		t.Errorf("401 count = %d, want 1", status401.Load())
	}
	if refreshCalls.Load() != 1 {
		t.Errorf("refresh count = %d, want 1", refreshCalls.Load())
	}
}

func TestCredentialExpirySkew(t *testing.T) {
	now := time.Now()
	// Expires in 30s: inside the 60s skew, so treated as expired.
	if !(Credential{Expires: now.Add(30 * time.Second).UnixMilli()}).Expired(now) {
		t.Error("token expiring inside the skew must be treated as expired")
	}
	if (Credential{Expires: now.Add(10 * time.Minute).UnixMilli()}).Expired(now) {
		t.Error("fresh token must not be expired")
	}
	// A credential with no expiry must not be treated as expired, so a
	// non-expiring setup does not refresh in a loop.
	if (Credential{}).Expired(now) {
		t.Error("zero expiry must not be expired")
	}
}

func TestStoreSaveCreatesFileWhenAbsent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "deep", "dir", "credential.json")
	s := &Store{Path: path}
	if err := s.Save(Credential{Type: "oauth", Access: "a", Refresh: "r", Expires: 1}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := s.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Access != "a" || got.Refresh != "r" {
		t.Errorf("round trip = %+v", got)
	}
}
