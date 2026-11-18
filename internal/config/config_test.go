package config

import (
	"os"
	"path/filepath"
	"testing"
)

// These tests pin the exact per-OS layout on every host, because the machine
// running them can only ever prove one of the three.
func TestResolvePerOS(t *testing.T) {
	home := filepath.FromSlash("/home/u")
	cases := []struct {
		name       string
		env        Env
		wantCred   string
		wantConfig string
		wantCache  string
	}{
		{
			name:       "linux defaults",
			env:        Env{GOOS: Linux, Home: home},
			wantCred:   filepath.FromSlash("/home/u/.local/share/opencode-console/credential.json"),
			wantConfig: filepath.FromSlash("/home/u/.config/opencode-limits/config.toml"),
			wantCache:  filepath.FromSlash("/home/u/.cache/opencode-limits"),
		},
		{
			name: "linux xdg overrides",
			env: Env{GOOS: Linux, Home: home,
				XDGDataHome:   filepath.FromSlash("/xdg/data"),
				XDGConfigHome: filepath.FromSlash("/xdg/config"),
				XDGCacheHome:  filepath.FromSlash("/xdg/cache")},
			wantCred:   filepath.FromSlash("/xdg/data/opencode-console/credential.json"),
			wantConfig: filepath.FromSlash("/xdg/config/opencode-limits/config.toml"),
			wantCache:  filepath.FromSlash("/xdg/cache/opencode-limits"),
		},
		{
			name:       "darwin",
			env:        Env{GOOS: Darwin, Home: home},
			wantCred:   filepath.FromSlash("/home/u/Library/Application Support/opencode-console/credential.json"),
			wantConfig: filepath.FromSlash("/home/u/Library/Application Support/opencode-limits/config.toml"),
			wantCache:  filepath.FromSlash("/home/u/Library/Caches/opencode-limits"),
		},
		{
			name:       "windows",
			env:        Env{GOOS: Windows, Home: home, LocalAppData: `C:\Users\u\AppData\Local`},
			wantCred:   filepath.Join(`C:\Users\u\AppData\Local`, "opencode-console", "credential.json"),
			wantConfig: filepath.Join(`C:\Users\u\AppData\Local`, "opencode-limits", "config.toml"),
			wantCache:  filepath.Join(`C:\Users\u\AppData\Local`, "opencode-limits", "Cache"),
		},
		{
			name:       "windows without LOCALAPPDATA",
			env:        Env{GOOS: Windows, Home: filepath.FromSlash("/home/u")},
			wantCred:   filepath.Join(filepath.FromSlash("/home/u"), "AppData", "Local", "opencode-console", "credential.json"),
			wantConfig: filepath.Join(filepath.FromSlash("/home/u"), "AppData", "Local", "opencode-limits", "config.toml"),
			wantCache:  filepath.Join(filepath.FromSlash("/home/u"), "AppData", "Local", "opencode-limits", "Cache"),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.env.Resolve()
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if got.CredentialFile != tc.wantCred {
				t.Errorf("credential path = %q, want %q", got.CredentialFile, tc.wantCred)
			}
			if got.ConfigFile != tc.wantConfig {
				t.Errorf("config path = %q, want %q", got.ConfigFile, tc.wantConfig)
			}
			if got.CacheDir != tc.wantCache {
				t.Errorf("cache path = %q, want %q", got.CacheDir, tc.wantCache)
			}
			if got.Server != DefaultServer {
				t.Errorf("server = %q, want %q", got.Server, DefaultServer)
			}
		})
	}
}

func TestResolveServerOverride(t *testing.T) {
	env := Env{GOOS: Linux, Home: "/home/u", Server: "http://127.0.0.1:9999"}
	got, err := env.Resolve()
	if err != nil {
		t.Fatal(err)
	}
	if got.Server != "http://127.0.0.1:9999" {
		t.Fatalf("server = %q", got.Server)
	}
}

func TestResolveNoHome(t *testing.T) {
	if _, err := (Env{GOOS: Linux}).Resolve(); err == nil {
		t.Fatal("expected error when home is empty")
	}
}

func TestLoadMissingReturnsErrNoConfig(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "absent.toml"))
	if err != ErrNoConfig {
		t.Fatalf("err = %v, want ErrNoConfig", err)
	}
}

func TestWriteThenLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	in := Config{
		Allocations:    map[string]float64{"gpt-6-luna": 15, "deepseek-v4.1-flash": 100, "glm-5.3": 200},
		RefreshSeconds: 30,
		WarningPercent: 60,
		DangerPercent:  90,
	}
	if err := Write(path, in); err != nil {
		t.Fatalf("Write: %v", err)
	}
	out, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(out.Allocations) != 3 || out.Allocations["glm-5.3"] != 200 {
		t.Errorf("allocations = %+v", out.Allocations)
	}
	if out.RefreshSeconds != 30 || out.WarningPercent != 60 || out.DangerPercent != 90 {
		t.Errorf("settings = %+v", out)
	}
}

func TestLoadRejectsUnknownKeys(t *testing.T) {
	// A typo'd model ID would silently drop an allocation, so it must fail loudly.
	path := filepath.Join(t.TempDir(), "config.toml")
	body := "refresh_seconds = 60\nwat = 1\n[allocations]\n\"m\" = 5\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("expected unknown-key error")
	}
}

func TestLoadAppliesDefaultsAndSwapsInvertedThresholds(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	// warning above danger: they should be swapped, not left inverted.
	body := "warning_percent = 95\ndanger_percent = 70\n[allocations]\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.WarningPercent != 70 || cfg.DangerPercent != 95 {
		t.Fatalf("thresholds = %d/%d, want 70/95", cfg.WarningPercent, cfg.DangerPercent)
	}
	if cfg.RefreshSeconds != 60 {
		t.Fatalf("refresh default = %d", cfg.RefreshSeconds)
	}
}

func TestWriteFileAtomicReplacesExisting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.txt")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WriteFileAtomic(path, []byte("new")); err != nil {
		t.Fatalf("WriteFileAtomic: %v", err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "new" {
		t.Fatalf("content = %q, want new", b)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", st.Mode().Perm())
	}
	// No temp files left behind.
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != "x.txt" {
			t.Errorf("leftover file: %s", e.Name())
		}
	}
}
