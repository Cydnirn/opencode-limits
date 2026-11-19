package update

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
)

// fakeRelease serves a release document and its assets. Every test points
// APIBase at this server, so no test touches the network.
type fakeRelease struct {
	Tag string
	// Binaries maps asset name to body. The sibling .sha256 asset is generated
	// from the body, matching what the release workflow publishes.
	Binaries map[string]string
	// OmitChecksum drops the .sha256 assets from the document.
	OmitChecksum bool
	// BadChecksum publishes a digest that does not match the binary.
	BadChecksum bool
}

func newFakeRelease(t *testing.T, f fakeRelease) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var downloads atomic.Int64
	if f.Tag == "" {
		f.Tag = "v0.2.0"
	}
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	assets := make([]Asset, 0, len(f.Binaries)*2)
	add := func(name string, handler http.HandlerFunc) {
		assets = append(assets, Asset{Name: name, URL: srv.URL + "/dl/" + name})
		mux.HandleFunc("/dl/"+name, handler)
	}
	for name, body := range f.Binaries {
		add(name, func(w http.ResponseWriter, r *http.Request) {
			downloads.Add(1)
			fmt.Fprint(w, body)
		})
		if f.OmitChecksum {
			continue
		}
		digest := fmt.Sprintf("%x", sha256.Sum256([]byte(body)))
		if f.BadChecksum {
			digest = strings.Repeat("ab", 32)
		}
		// sha256sum format: the digest, two spaces, then the file name — the
		// exact shape the published .sha256 assets have.
		line := digest + "  " + name + "\n"
		add(name+".sha256", func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, line)
		})
	}

	mux.HandleFunc("/repos/Cydnirn/opencode-limits/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") == "" {
			http.Error(w, "no user agent", http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(Release{Tag: f.Tag, HTMLURL: srv.URL + "/release", Assets: assets})
	})
	return srv, &downloads
}

// newTestClient points a client at the fake server and a platform that has a
// published asset in every fixture.
func newTestClient(srv *httptest.Server) *Client {
	return &Client{
		HTTP:    srv.Client(),
		APIBase: srv.URL,
		GOOS:    "linux",
		GOARCH:  "amd64",
	}
}

func TestCompare(t *testing.T) {
	tests := []struct {
		name string
		a, b string
		want int
	}{
		{name: "equal", a: "v0.1.1", b: "v0.1.1", want: 0},
		{name: "bare equals prefixed", a: "0.1.1", b: "v0.1.1", want: 0},
		{name: "missing patch equals zero", a: "v0.1", b: "v0.1.0", want: 0},
		{name: "missing minor", a: "v1", b: "v1.0.0", want: 0},
		{name: "newer patch", a: "v0.1.2", b: "v0.1.1", want: 1},
		{name: "older patch", a: "v0.1.0", b: "v0.1.1", want: -1},
		// Lexical comparison gets this pair backwards; numeric must not.
		{name: "patch 10 beats 9", a: "v0.1.10", b: "v0.1.9", want: 1},
		{name: "patch 9 loses to 10", a: "v0.1.9", b: "v0.1.10", want: -1},
		{name: "minor 2 beats minor 1 patch 99", a: "v0.2.0", b: "v0.1.99", want: 1},
		{name: "minor 1 patch 99 loses to minor 2", a: "v0.1.99", b: "v0.2.0", want: -1},
		{name: "major wins", a: "v1.0.0", b: "v0.99.99", want: 1},
		{name: "bare newer", a: "0.1.2", b: "0.1.1", want: 1},
		{name: "uppercase v", a: "V0.1.2", b: "v0.1.1", want: 1},
		{name: "prerelease is older than release", a: "v0.2.0-rc.1", b: "v0.2.0", want: -1},
		{name: "release is newer than prerelease", a: "v0.2.0", b: "v0.2.0-rc.1", want: 1},
		{name: "prerelease numeric ordering", a: "v0.2.0-rc.10", b: "v0.2.0-rc.9", want: 1},
		{name: "prerelease numeric beats alpha", a: "v0.2.0-1", b: "v0.2.0-alpha", want: -1},
		{name: "prerelease longer wins", a: "v0.2.0-alpha.1", b: "v0.2.0-alpha", want: 1},
		{name: "build metadata ignored", a: "v0.2.0+build.7", b: "v0.2.0", want: 0},
		// A `git describe` build is the tag plus commits, so it must not be
		// ordered below the tag it descends from.
		{name: "describe snapshot equals its tag", a: "v0.1.0-5-g384465a", b: "v0.1.0", want: 0},
		{name: "dirty describe snapshot equals its tag", a: "v0.1.0-5-g384465a-dirty", b: "v0.1.0", want: 0},
		// make build with uncommitted changes stamps exactly this.
		{name: "bare dirty equals its tag", a: "v0.1.1-dirty", b: "v0.1.1", want: 0},
		{name: "bare dirty loses to the next release", a: "v0.1.1-dirty", b: "v0.1.2", want: -1},
		{name: "describe snapshot loses to the next release", a: "v0.1.0-5-g384465a", b: "v0.1.1", want: -1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Compare(tt.a, tt.b)
			if err != nil {
				t.Fatalf("Compare(%q, %q): %v", tt.a, tt.b, err)
			}
			if got != tt.want {
				t.Errorf("Compare(%q, %q) = %d, want %d", tt.a, tt.b, got, tt.want)
			}
			// Ordering must be antisymmetric, or --check-update flips on retry.
			if back, err := Compare(tt.b, tt.a); err != nil || back != -tt.want {
				t.Errorf("Compare(%q, %q) = %d, want %d", tt.b, tt.a, back, -tt.want)
			}
		})
	}
}

func TestCompareRejectsGarbage(t *testing.T) {
	tests := []struct {
		name string
		a, b string
		want error
	}{
		{name: "dev sentinel", a: DevVersion, b: "v0.1.1", want: ErrDevBuild},
		{name: "dev on either side", a: "v0.1.1", b: "dev", want: ErrDevBuild},
		{name: "not a number", a: "v0.x.1", b: "v0.1.1", want: ErrBadVersion},
		{name: "empty", a: "", b: "v0.1.1", want: ErrBadVersion},
		{name: "four fields", a: "v0.1.1.1", b: "v0.1.1", want: ErrBadVersion},
		{name: "negative", a: "v0.-1.1", b: "v0.1.1", want: ErrBadVersion},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := Compare(tt.a, tt.b); !errors.Is(err, tt.want) {
				t.Errorf("Compare(%q, %q) error = %v, want %v", tt.a, tt.b, err, tt.want)
			}
		})
	}
}

func TestIsNewer(t *testing.T) {
	tests := []struct {
		name    string
		current string
		latest  string
		want    bool
	}{
		{name: "current is stale", current: "v0.1.0", latest: "v0.1.1", want: true},
		{name: "current is newest", current: "v0.1.1", latest: "v0.1.0", want: false},
		{name: "same version", current: "v0.1.1", latest: "v0.1.1", want: false},
		{name: "stale describe snapshot", current: "v0.1.0-5-g384465a", latest: "v0.1.1", want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := IsNewer(tt.current, tt.latest)
			if err != nil {
				t.Fatalf("IsNewer(%q, %q): %v", tt.current, tt.latest, err)
			}
			if got != tt.want {
				t.Errorf("IsNewer(%q, %q) = %v, want %v", tt.current, tt.latest, got, tt.want)
			}
		})
	}
}

// The names here are copied from `gh release view v0.1.0 --json assets`, so a
// change to the release workflow fails this test instead of shipping an updater
// that cannot find its own binary.
func TestAssetName(t *testing.T) {
	tests := []struct {
		goos, goarch string
		tag          string
		want         string
	}{
		{goos: "linux", goarch: "amd64", tag: "v0.1.1", want: "opencode-limits_v0.1.1_linux_amd64"},
		{goos: "linux", goarch: "arm64", tag: "v0.1.1", want: "opencode-limits_v0.1.1_linux_arm64"},
		{goos: "darwin", goarch: "amd64", tag: "v0.1.1", want: "opencode-limits_v0.1.1_darwin_amd64"},
		{goos: "darwin", goarch: "arm64", tag: "v0.1.1", want: "opencode-limits_v0.1.1_darwin_arm64"},
		{goos: "windows", goarch: "amd64", tag: "v0.1.1", want: "opencode-limits_v0.1.1_windows_amd64.exe"},
		{goos: "windows", goarch: "arm64", tag: "v0.1.1", want: "opencode-limits_v0.1.1_windows_arm64.exe"},
		// A bare tag is normalised to the published, v-prefixed form.
		{goos: "linux", goarch: "amd64", tag: "0.1.1", want: "opencode-limits_v0.1.1_linux_amd64"},
	}
	for _, tt := range tests {
		t.Run(tt.goos+"_"+tt.goarch+"/"+tt.tag, func(t *testing.T) {
			if got := AssetName(tt.tag, tt.goos, tt.goarch); got != tt.want {
				t.Errorf("AssetName(%q, %q, %q) = %q, want %q", tt.tag, tt.goos, tt.goarch, got, tt.want)
			}
		})
	}
}

func TestSupported(t *testing.T) {
	tests := []struct {
		goos, goarch string
		want         bool
	}{
		{goos: "linux", goarch: "amd64", want: true},
		{goos: "linux", goarch: "arm64", want: true},
		{goos: "darwin", goarch: "amd64", want: true},
		{goos: "darwin", goarch: "arm64", want: true},
		{goos: "windows", goarch: "amd64", want: true},
		{goos: "windows", goarch: "arm64", want: true},
		{goos: "freebsd", goarch: "amd64", want: false},
		{goos: "linux", goarch: "386", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.goos+"_"+tt.goarch, func(t *testing.T) {
			if got := Supported(tt.goos, tt.goarch); got != tt.want {
				t.Errorf("Supported(%q, %q) = %v, want %v", tt.goos, tt.goarch, got, tt.want)
			}
		})
	}
}

func TestParseSHA256(t *testing.T) {
	const digest = "b9793744a4f3fb916b35039fe4c4a444dd336e1ba90a3fe6d6e13d48099c12cb"
	tests := []struct {
		name  string
		input string
		want  string
		isErr bool
	}{
		{name: "sha256sum output", input: digest + "  opencode-limits_v0.1.0_linux_amd64\n", want: digest},
		{name: "bare digest", input: digest + "\n", want: digest},
		{name: "uppercase hex", input: strings.ToUpper(digest) + "\n", want: digest},
		{name: "leading whitespace", input: "  " + digest, want: digest},
		{name: "empty", input: "", isErr: true},
		{name: "too short", input: "b9793744", isErr: true},
		{name: "not hex", input: strings.Repeat("z", 64), isErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseSHA256(tt.input)
			if tt.isErr {
				if err == nil {
					t.Fatalf("ParseSHA256(%q) = %q, want error", tt.input, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseSHA256(%q): %v", tt.input, err)
			}
			if got != tt.want {
				t.Errorf("ParseSHA256 = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestVerifySHA256(t *testing.T) {
	data := []byte("binary payload")
	good := fmt.Sprintf("%x", sha256.Sum256(data))
	tests := []struct {
		name string
		data []byte
		want string
		isOK bool
	}{
		{name: "match", data: data, want: good, isOK: true},
		{name: "uppercase digest", data: data, want: strings.ToUpper(good), isOK: true},
		{name: "mismatch", data: data, want: strings.Repeat("ab", 32), isOK: false},
		{name: "truncated content", data: data[:4], want: good, isOK: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := VerifySHA256(tt.data, tt.want)
			if tt.isOK && err != nil {
				t.Fatalf("VerifySHA256: %v", err)
			}
			if !tt.isOK && err == nil {
				t.Fatal("VerifySHA256 accepted a bad digest")
			}
		})
	}
}

func TestCheckReportsAvailableWithoutDownloading(t *testing.T) {
	name := AssetName("v0.2.0", "linux", "amd64")
	srv, downloads := newFakeRelease(t, fakeRelease{
		Tag:      "v0.2.0",
		Binaries: map[string]string{name: "new binary"},
	})

	out, err := newTestClient(srv).Check(context.Background(), "v0.1.1")
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !out.Newer || out.Changed || out.Applied {
		t.Errorf("outcome = %+v, want newer but nothing changed", out)
	}
	if out.Latest != "v0.2.0" || out.AssetName != name {
		t.Errorf("latest = %q, asset = %q", out.Latest, out.AssetName)
	}
	if out.AssetURL == "" {
		t.Error("no download URL; a check-only run should still say where to get the file")
	}
	// --check-update must never download a binary.
	if downloads.Load() != 0 {
		t.Errorf("%d binary downloads, want 0", downloads.Load())
	}
}

func TestApplyDevBuildRefuses(t *testing.T) {
	srv, downloads := newFakeRelease(t, fakeRelease{
		Binaries: map[string]string{AssetName("v0.2.0", "linux", "amd64"): "x"},
	})
	dir := t.TempDir()
	target := filepath.Join(dir, "opencode-limits")
	if err := os.WriteFile(target, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}

	out, err := newTestClient(srv).Apply(context.Background(), DevVersion, Installer{Path: target, GOOS: "linux"})
	if !errors.Is(err, ErrDevBuild) {
		t.Fatalf("error = %v, want ErrDevBuild", err)
	}
	if out.Changed {
		t.Error("a dev build must not report a change")
	}
	if downloads.Load() != 0 {
		t.Errorf("%d downloads, want 0: a dev build must not touch the network for an asset", downloads.Load())
	}
	if got, _ := os.ReadFile(target); string(got) != "old" {
		t.Errorf("binary was modified: %q", got)
	}
}

func TestApplyAlreadyCurrent(t *testing.T) {
	bin := "new binary"
	srv, downloads := newFakeRelease(t, fakeRelease{
		Tag: "v0.1.1",
		Binaries: map[string]string{
			AssetName("v0.1.1", "linux", "amd64"): bin,
		},
	})
	dir := t.TempDir()
	target := filepath.Join(dir, "opencode-limits")
	if err := os.WriteFile(target, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}

	out, err := newTestClient(srv).Apply(context.Background(), "v0.1.1", Installer{Path: target, GOOS: "linux"})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if out.Changed || out.Applied {
		t.Errorf("outcome = %+v, want no change", out)
	}
	if out.Latest != "v0.1.1" {
		t.Errorf("latest = %q", out.Latest)
	}
	if downloads.Load() != 0 {
		t.Errorf("%d downloads when already current, want 0", downloads.Load())
	}
	if got, _ := os.ReadFile(target); string(got) != "old" {
		t.Errorf("binary was modified: %q", got)
	}
}

func TestApplyInstallsVerifiedBinary(t *testing.T) {
	name := AssetName("v0.2.0", "linux", "amd64")
	srv, _ := newFakeRelease(t, fakeRelease{
		Tag:      "v0.2.0",
		Binaries: map[string]string{name: "new binary"},
	})
	dir := t.TempDir()
	target := filepath.Join(dir, "opencode-limits")
	if err := os.WriteFile(target, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}

	out, err := newTestClient(srv).Apply(context.Background(), "v0.1.1", Installer{Path: target, GOOS: "linux"})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !out.Changed || !out.Applied {
		t.Fatalf("outcome = %+v, want changed and applied", out)
	}
	if out.Path != target {
		t.Errorf("path = %q, want %q", out.Path, target)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "new binary" {
		t.Errorf("content = %q", got)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	// Windows derives os.FileMode from file attributes, not ACLs, and reports 0666
	// for any writable file regardless of chmod; asserting execute bits there
	// tests the OS, not this code. Executability on Windows comes from the .exe
	// extension.
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o111 == 0 {
		t.Errorf("mode = %v, want executable", info.Mode().Perm())
	}
	// The temp file must not survive the rename.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("directory holds %v, want only the binary", names)
	}
}

func TestApplyChecksumMismatchAborts(t *testing.T) {
	name := AssetName("v0.2.0", "linux", "amd64")
	srv, _ := newFakeRelease(t, fakeRelease{
		Tag:         "v0.2.0",
		BadChecksum: true,
		Binaries:    map[string]string{name: "new binary"},
	})
	dir := t.TempDir()
	target := filepath.Join(dir, "opencode-limits")
	if err := os.WriteFile(target, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}

	out, err := newTestClient(srv).Apply(context.Background(), "v0.1.1", Installer{Path: target, GOOS: "linux"})
	if err == nil {
		t.Fatal("expected a checksum error")
	}
	if !strings.Contains(err.Error(), "checksum mismatch") {
		t.Errorf("error = %v, want a checksum mismatch", err)
	}
	if out.Applied {
		t.Error("nothing must be installed when the checksum fails")
	}
	if got, _ := os.ReadFile(target); string(got) != "old" {
		t.Errorf("binary was replaced despite a bad checksum: %q", got)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("left %d files behind, want 1", len(entries))
	}
}

func TestApplyMissingChecksumAssetAborts(t *testing.T) {
	name := AssetName("v0.2.0", "linux", "amd64")
	srv, downloads := newFakeRelease(t, fakeRelease{
		Tag:          "v0.2.0",
		OmitChecksum: true,
		Binaries:     map[string]string{name: "new binary"},
	})
	dir := t.TempDir()
	target := filepath.Join(dir, "opencode-limits")
	if err := os.WriteFile(target, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}

	_, err := newTestClient(srv).Apply(context.Background(), "v0.1.1", Installer{Path: target, GOOS: "linux"})
	if err == nil {
		t.Fatal("expected an error when no .sha256 is published")
	}
	if !strings.Contains(err.Error(), "cannot be verified") {
		t.Errorf("error = %v, want a message about verification", err)
	}
	if downloads.Load() != 0 {
		t.Errorf("%d binary downloads, want 0: refuse before downloading", downloads.Load())
	}
	if got, _ := os.ReadFile(target); string(got) != "old" {
		t.Errorf("binary was replaced without a checksum: %q", got)
	}
}

func TestApplyUnsupportedPlatform(t *testing.T) {
	srv, _ := newFakeRelease(t, fakeRelease{
		Binaries: map[string]string{AssetName("v0.2.0", "linux", "amd64"): "x"},
	})
	c := newTestClient(srv)
	c.GOOS, c.GOARCH = "freebsd", "386"

	_, err := c.Apply(context.Background(), "v0.1.1", Installer{Path: filepath.Join(t.TempDir(), "b"), GOOS: "freebsd"})
	if err == nil || !strings.Contains(err.Error(), "no binary for freebsd/386") {
		t.Fatalf("error = %v, want an unsupported-platform message", err)
	}
}

// Windows cannot replace a running .exe, so the download must be staged beside
// it with a name the user can run themselves.
func TestInstallWindowsStagesPendingFile(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "opencode-limits.exe")
	if err := os.WriteFile(target, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	in := Installer{Path: target, GOOS: "windows"}

	path, applied, err := in.Install([]byte("new binary"))
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if applied {
		t.Error("a Windows install must not claim to be applied")
	}
	if want := filepath.Join(dir, "opencode-limits.new.exe"); path != want {
		t.Errorf("staged at %q, want %q", path, want)
	}
	if got, _ := os.ReadFile(target); string(got) != "old" {
		t.Errorf("running binary was modified: %q", got)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != "new binary" {
		t.Errorf("staged file = %q (err %v)", got, err)
	}
}

func TestPendingPath(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{in: "/usr/local/bin/opencode-limits", want: "/usr/local/bin/opencode-limits.new"},
		{in: `C:\bin\opencode-limits.exe`, want: `C:\bin\opencode-limits.new.exe`},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			if got := PendingPath(tt.in); got != tt.want {
				t.Errorf("PendingPath(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// A read-only install directory must produce a usable instruction, not a bare
// permission error. Root ignores the mode bits, so the case is skipped there.
func TestInstallNotWritableReportsCommand(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory permissions")
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "opencode-limits")
	// Making a directory read-only via chmod only means something on Unix. On
	// Windows the write still succeeds (ACLs, not mode bits, decide), so the
	// scenario this test sets up cannot exist there.
	if runtime.GOOS == "windows" {
		t.Skip("chmod cannot make a directory read-only on Windows; ACLs decide")
	}
	if err := os.WriteFile(target, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	_, _, err := Installer{Path: target, GOOS: "linux"}.Install([]byte("new binary"))
	if err == nil {
		t.Fatal("expected a permissions error")
	}
	if !errors.Is(err, os.ErrPermission) {
		t.Errorf("error does not wrap ErrPermission: %v", err)
	}
	var nw *NotWritableError
	if !errors.As(err, &nw) {
		t.Fatalf("error type = %T, want *NotWritableError", err)
	}
	t.Cleanup(func() {
		if nw.SavedTo != "" {
			_ = os.Remove(nw.SavedTo)
		}
	})
	if nw.SavedTo == "" {
		t.Fatal("no copy of the verified download was saved")
	}
	msg := err.Error()
	for _, want := range []string{target, nw.SavedTo, "sudo install -m 0755"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message %q lacks %q", msg, want)
		}
	}
	if got, _ := os.ReadFile(target); string(got) != "old" {
		t.Errorf("binary was modified: %q", got)
	}
}

// The check must be usable without credentials: a 403 from the API has to reach
// the user with its reason, not as a bare status code.
func TestLatestSurfacesAPIError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/Cydnirn/opencode-limits/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, `{"message":"API rate limit exceeded"}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	_, err := newTestClient(srv).Latest(context.Background())
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "403") || !strings.Contains(err.Error(), "rate limit") {
		t.Errorf("error = %v, want status and reason", err)
	}
}

func TestLatestSendsTokenAndUserAgent(t *testing.T) {
	var gotAuth, gotUA atomic.Value
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/Cydnirn/opencode-limits/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		gotAuth.Store(r.Header.Get("Authorization"))
		gotUA.Store(r.Header.Get("User-Agent"))
		fmt.Fprint(w, `{"tag_name":"v9.9.9","assets":[]}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := newTestClient(srv)
	c.Token = "tok"
	if _, err := c.Latest(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got, _ := gotAuth.Load().(string); got != "Bearer tok" {
		t.Errorf("Authorization = %q", got)
	}
	if got, _ := gotUA.Load().(string); got == "" {
		t.Error("no User-Agent sent; GitHub rejects such requests")
	}
}

func TestNewInstallerResolvesExecutable(t *testing.T) {
	in, err := NewInstaller()
	if err != nil {
		t.Fatalf("NewInstaller: %v", err)
	}
	if in.Path == "" || !filepath.IsAbs(in.Path) {
		t.Errorf("path = %q, want an absolute path", in.Path)
	}
	if in.GOOS != runtime.GOOS {
		t.Errorf("GOOS = %q, want %q", in.GOOS, runtime.GOOS)
	}
}

func TestNewClientDefaultsToHost(t *testing.T) {
	c := NewClient()
	if c.GOOS != runtime.GOOS || c.GOARCH != runtime.GOARCH {
		t.Errorf("platform = %s/%s, want %s/%s", c.GOOS, c.GOARCH, runtime.GOOS, runtime.GOARCH)
	}
	if c.APIBase != "" {
		t.Errorf("APIBase = %q, want empty so the default applies", c.APIBase)
	}
	if c.apiBase() != DefaultAPIBase {
		t.Errorf("apiBase() = %q", c.apiBase())
	}
}
