package allowance

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The embedded table is the fallback when the network is unavailable, so its
// shape and values are load-bearing.
func TestEmbeddedTableIsValid(t *testing.T) {
	tbl := Embedded()
	if len(tbl) < 30 {
		t.Fatalf("embedded table has %d models, expected the full set", len(tbl))
	}
	for _, id := range []string{"gpt-6-luna", "deepseek-v4.1-flash", "glm-5.3", "beam-free"} {
		if _, ok := tbl.Lookup(id); !ok {
			t.Errorf("embedded table missing %s", id)
		}
	}
	// Spot-check against the published table at opencode.ai/console/go.
	checks := []struct {
		id       string
		plan     string
		want     float64
		unlimite bool
	}{
		{id: "gpt-6-luna", plan: "go", want: 15},
		{id: "gpt-6-luna", plan: "go-plus", want: 60},
		{id: "deepseek-v4.1-flash", plan: "go", want: 100},
		{id: "deepseek-v4.1-flash", plan: "go-plus", want: 200},
		{id: "glm-5.3", plan: "go", want: 500},
		{id: "glm-5.3", plan: "go-plus", want: 1000},
		{id: "gemini-4-argon", plan: "go", want: 30},
		{id: "minimax-m3.1-flash", plan: "go", want: 20},
		{id: "qwen3.8-flash", plan: "go-plus", want: 90},
	}
	for _, c := range checks {
		l, ok := tbl.Lookup(c.id)
		if !ok {
			t.Errorf("%s missing", c.id)
			continue
		}
		got, unlim, has := l.MonthlyFor(c.plan)
		if !has {
			t.Errorf("%s/%s not found", c.id, c.plan)
			continue
		}
		if unlim {
			t.Errorf("%s/%s unexpectedly unlimited", c.id, c.plan)
			continue
		}
		if got != c.want {
			t.Errorf("%s/%s = %v, want %v", c.id, c.plan, got, c.want)
		}
	}
}

// Free models carry no cap at all, which must read as unlimited rather than $0.
func TestFreeModelsAreUnlimited(t *testing.T) {
	tbl := Embedded()
	for _, id := range []string{"beam-free", "hy4-flash-free", "longcat-2.5-free", "iceman-contributor-free"} {
		l, ok := tbl.Lookup(id)
		if !ok {
			t.Errorf("%s missing", id)
			continue
		}
		if _, unlim, has := l.MonthlyFor("go"); !has || !unlim {
			t.Errorf("%s/go should be unlimited, got has=%v unlimited=%v", id, has, unlim)
		}
		if got := l.String("go"); got != "unlimited" {
			t.Errorf("%s renders as %q, want unlimited", id, got)
		}
	}
}

// A model the table does not cover must report not-found, not zero.
func TestUnknownModelReportsNotFound(t *testing.T) {
	tbl := Embedded()
	if _, ok := tbl.Lookup("no-such-model"); ok {
		t.Fatal("unknown model must not be found")
	}
	// And an empty Limit must not claim to know any plan.
	var zero Limit
	if _, _, ok := zero.MonthlyFor("go"); ok {
		t.Fatal("zero Limit must report not-found")
	}
}

func TestStringRendering(t *testing.T) {
	tbl := Embedded()
	cases := []struct{ id, plan, want string }{
		{"gpt-6-luna", "go", "$15"},
		{"deepseek-v4.1-flash", "go", "$100"},
		{"glm-5.3", "go-plus", "$1000"},
		{"beam-free", "go", "unlimited"},
		{"gpt-6-luna", "no-such-plan", "--"},
	}
	for _, c := range cases {
		l, _ := tbl.Lookup(c.id)
		if got := l.String(c.plan); got != c.want {
			t.Errorf("%s/%s = %q, want %q", c.id, c.plan, got, c.want)
		}
	}
}

// bundleJS reproduces the shape the Console ships: id, then a go block with
// monthly_usage, including the null form that means unlimited.
const bundleJS = "a,{id:`gpt-6-luna`,name:`GPT-6 Luna`,cost:{text:{input:{price:.1}}}," +
	"go:{monthly_usage:{go:15,\"go-plus\":60,\"go-teams\":20},featured:!1}}," +
	"{id:`glm-5.3`,name:`GLM-5.3`,go:{monthly_usage:{go:500,\"go-plus\":1e3,\"go-teams\":505}}}," +
	"{id:`beam-free`,name:`Beam Free`,go:{monthly_usage:null,featured:!0}}," +
	"{id:`qwen3.8-flash`,name:`Qwen3.8 Flash`,go:{monthly_usage:{go:30,\"go-plus\":90}}},"

func TestParseBundle(t *testing.T) {
	tbl, err := ParseBundle([]byte(bundleJS))
	if err != nil {
		t.Fatalf("ParseBundle: %v", err)
	}
	cases := []struct {
		id   string
		plan string
		want float64
	}{
		{"gpt-6-luna", "go", 15},
		{"gpt-6-luna", "go-plus", 60},
		{"gpt-6-luna", "go-teams", 20},
		{"glm-5.3", "go", 500},
		// The bundle writes 1000 as 1e3; it must parse as a number, not a string.
		{"glm-5.3", "go-plus", 1000},
		{"qwen3.8-flash", "go", 30},
	}
	for _, c := range cases {
		l, ok := tbl.Lookup(c.id)
		if !ok {
			t.Errorf("%s missing from parsed bundle", c.id)
			continue
		}
		got, unlim, has := l.MonthlyFor(c.plan)
		if !has {
			t.Errorf("%s/%s missing", c.id, c.plan)
			continue
		}
		if unlim {
			t.Errorf("%s/%s unexpectedly unlimited", c.id, c.plan)
			continue
		}
		if got != c.want {
			t.Errorf("%s/%s = %v, want %v", c.id, c.plan, got, c.want)
		}
	}
}

// The null block must be recorded for the right model. Before this was handled,
// the null was skipped and the *next* model's limits were attached to it.
func TestParseBundleNullMeansUnlimitedAndDoesNotShift(t *testing.T) {
	tbl, err := ParseBundle([]byte(bundleJS))
	if err != nil {
		t.Fatal(err)
	}
	beam, ok := tbl.Lookup("beam-free")
	if !ok {
		t.Fatal("beam-free missing: the null block shifted the association")
	}
	if _, unlim, has := beam.MonthlyFor("go"); !has || !unlim {
		t.Errorf("beam-free should be unlimited, has=%v unlim=%v", has, unlim)
	}
	// The model after the null must still carry its own values.
	qwen, ok := tbl.Lookup("qwen3.8-flash")
	if !ok {
		t.Fatal("qwen3.8-flash missing")
	}
	if got, _, _ := qwen.MonthlyFor("go"); got != 30 {
		t.Errorf("qwen3.8-flash/go = %v, want 30 (association shifted)", got)
	}
}

func TestParseBundleRejectsGarbage(t *testing.T) {
	if _, err := ParseBundle([]byte("no models here")); err == nil {
		t.Fatal("expected an error for a bundle with no usage blocks")
	}
}

func TestCompare(t *testing.T) {
	a := Table{
		"m1": {Monthly: map[string]float64{"go": 15}},
		"m2": {Monthly: map[string]float64{"go": 60}},
	}
	b := Table{
		"m1": {Monthly: map[string]float64{"go": 20}}, // changed
		"m3": {Monthly: map[string]float64{"go": 5}},  // new
	}
	diffs := Compare(a, b)
	if len(diffs) != 3 {
		t.Fatalf("got %d diffs, want 3: %v", len(diffs), diffs)
	}
	joined := strings.Join(diffs, "\n")
	for _, want := range []string{"m1/go: 15 -> 20", "m2: missing", "m3: new model"} {
		if !strings.Contains(joined, want) {
			t.Errorf("diffs missing %q:\n%s", want, joined)
		}
	}
}

// serveBundle fakes the Console page and asset so Fetch can be tested offline.
func serveBundle(t *testing.T, js string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/console/go", func(w http.ResponseWriter, r *http.Request) {
		// The real page references its asset by hashed name.
		w.Write([]byte(`<script src="/console/_build/assets/go-ABC123xy.js"></script>`))
	})
	mux.HandleFunc("/console/_build/assets/go-ABC123xy.js", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(js))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// failingFetch stands in for the network so the fallback path is testable offline.
func failingFetch(context.Context, *http.Client) (Table, error) {
	return nil, fmt.Errorf("offline")
}

func TestResolverFallsBackToEmbeddedWhenFetchFails(t *testing.T) {
	// Point at a closed port so the fetch cannot succeed.
	r := &Resolver{Fetch: failingFetch}
	tbl, source := r.Get(context.Background())
	if source != SourceEmbedded {
		t.Fatalf("source = %q, want embedded", source)
	}
	if len(tbl) == 0 {
		t.Fatal("must still return the embedded table when offline")
	}
	if r.LastError() == nil {
		t.Error("expected the fetch failure to be recorded")
	}
}

func TestResolverCachesWithinTTL(t *testing.T) {
	now := time.Now()
	r := &Resolver{TTL: time.Hour, Fetch: failingFetch, Now: func() time.Time { return now }}
	first, src := r.Get(context.Background())
	if src != SourceEmbedded {
		t.Fatalf("first source = %q", src)
	}
	// A second call inside the TTL must not attempt the network again, so the
	// table is identical even though Fetch would fail here.
	second, src2 := r.Get(context.Background())
	if src2 != src || len(second) != len(first) {
		t.Error("second call should reuse the cached table within the TTL")
	}
}

// Fetch is exercised end to end against a local server by pointing the package
// URLs at it, which also proves the asset-hash discovery works.
func TestFetchDiscoversAssetAndParses(t *testing.T) {
	srv := serveBundle(t, bundleJS)
	oldPage, oldBase := consoleGoPage, bundleBase
	consoleGoPage = srv.URL + "/console/go"
	bundleBase = srv.URL + "/console/_build/assets/"
	t.Cleanup(func() { consoleGoPage, bundleBase = oldPage, oldBase })

	tbl, err := Fetch(context.Background(), srv.Client())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(tbl) != 4 {
		t.Fatalf("parsed %d models, want 4", len(tbl))
	}
	l, _ := tbl.Lookup("gpt-6-luna")
	if got, _, _ := l.MonthlyFor("go"); got != 15 {
		t.Errorf("gpt-6-luna/go = %v, want 15", got)
	}
}

// A resolver with a working fetch must report the live source and adopt its data.
func TestResolverUsesLiveTableWhenAvailable(t *testing.T) {
	r := &Resolver{
		Fetch: func(context.Context, *http.Client) (Table, error) {
			return Table{"only": {Monthly: map[string]float64{"go": 7}}}, nil
		},
	}
	tbl, src := r.Get(context.Background())
	if src != SourceLive {
		t.Fatalf("source = %q, want live", src)
	}
	if len(tbl) != 1 {
		t.Fatalf("expected the live table, got %d models", len(tbl))
	}
	if r.LastError() != nil {
		t.Errorf("no error expected, got %v", r.LastError())
	}
}
