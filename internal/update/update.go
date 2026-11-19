// Package update checks a GitHub release feed and replaces the running binary
// with a newer one.
//
// Only the version stamped into the binary at build time can decide whether an
// update is needed: nothing on disk records what was installed, and comparing
// file contents would make every locally built binary look stale. That is also
// why a `dev` build refuses to update — it has no version to compare and no
// release asset that matches it, so "updating" it would quietly replace a
// working-tree build with a downloaded release.
//
// Everything here is stdlib. The feed is one JSON document and the asset names
// are fixed by the release workflow, so neither a semantic-versioning nor a
// release-client dependency earns its keep; the comparison below is deliberately
// small enough to read in one sitting.
package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	// Owner and Repo name the releases this tool updates from. They are not
	// configurable: a binary that can be pointed at another repository is a
	// binary that can be made to install anything.
	Owner = "Cydnirn"
	Repo  = "opencode-limits"

	// DevVersion is what the Makefile stamps when git describe yields nothing.
	DevVersion = "dev"

	// DefaultAPIBase is the public GitHub API.
	DefaultAPIBase = "https://api.github.com"

	// GitHub rejects a request with no User-Agent, with a 403 whose body reads
	// like an authentication failure.
	userAgent = "opencode-limits-updater"

	// assetPrefix is shared by every published binary.
	assetPrefix = "opencode-limits_"

	// maxAssetBytes bounds a download. Published binaries are well under 20 MiB,
	// so anything far beyond that is a wrong URL or a hostile response.
	maxAssetBytes = 64 << 20

	// maxDocBytes bounds the release document and the checksum asset.
	maxDocBytes = 1 << 20
)

var (
	// ErrDevBuild reports that the running binary has no release version.
	ErrDevBuild = errors.New("dev build has no release version")

	// ErrBadVersion reports a tag that cannot be ordered.
	ErrBadVersion = errors.New("unrecognised version")
)

// defaultHTTP is used when the caller supplies no client. A timeout matters
// here more than usual: a self-update that hangs on a stalled connection leaves
// the operator with no binary and no message.
var defaultHTTP = &http.Client{Timeout: 30 * time.Second}

// Asset is one file attached to a release.
type Asset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
}

// Release is the subset of the GitHub release document this tool reads.
type Release struct {
	Tag     string  `json:"tag_name"`
	HTMLURL string  `json:"html_url"`
	Assets  []Asset `json:"assets"`
}

// Asset finds an asset by exact name.
func (r Release) Asset(name string) (Asset, bool) {
	for _, a := range r.Assets {
		if a.Name == name {
			return a, true
		}
	}
	return Asset{}, false
}

// assetNames lists every published asset name, sorted, for error messages that
// say what the release actually contains.
func (r Release) assetNames() []string {
	names := make([]string, 0, len(r.Assets))
	for _, a := range r.Assets {
		names = append(names, a.Name)
	}
	sort.Strings(names)
	return names
}

// Client talks to the release feed. The zero value is usable and updates for
// the host platform; every field is an override, which is what makes the tests
// hermetic.
type Client struct {
	// HTTP carries every request. Nil means defaultHTTP.
	HTTP *http.Client
	// APIBase replaces the GitHub API origin. Tests point it at httptest.
	APIBase string
	// Token is sent as a bearer token when set. Empty means GITHUB_TOKEN is
	// read from the environment, because the API is rate-limited per IP and
	// unauthenticated checks are the ones that get throttled.
	Token string
	// GOOS and GOARCH select the asset. Empty means the host platform.
	GOOS   string
	GOARCH string
}

// NewClient returns a client for the host platform, reading GITHUB_TOKEN from
// the environment.
func NewClient() *Client {
	return &Client{
		HTTP:   defaultHTTP,
		GOOS:   runtime.GOOS,
		GOARCH: runtime.GOARCH,
	}
}

func (c *Client) http() *http.Client {
	if c == nil || c.HTTP == nil {
		return defaultHTTP
	}
	return c.HTTP
}

func (c *Client) apiBase() string {
	if c == nil || c.APIBase == "" {
		return DefaultAPIBase
	}
	return strings.TrimSuffix(c.APIBase, "/")
}

func (c *Client) token() string {
	if c != nil && c.Token != "" {
		return c.Token
	}
	return os.Getenv("GITHUB_TOKEN")
}

func (c *Client) goos() string {
	if c == nil || c.GOOS == "" {
		return runtime.GOOS
	}
	return c.GOOS
}

func (c *Client) goarch() string {
	if c == nil || c.GOARCH == "" {
		return runtime.GOARCH
	}
	return c.GOARCH
}

// get issues one request with the headers GitHub requires.
func (c *Client) get(ctx context.Context, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("build request for %s: %w", url, err)
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/vnd.github+json")
	if t := c.token(); t != "" {
		req.Header.Set("Authorization", "Bearer "+t)
	}
	resp, err := c.http().Do(req)
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", url, err)
	}
	return resp, nil
}

// Latest fetches the newest published release.
//
// The endpoint deliberately excludes drafts and prereleases, so a release
// candidate never becomes the update target.
func (c *Client) Latest(ctx context.Context) (Release, error) {
	url := c.apiBase() + "/repos/" + Owner + "/" + Repo + "/releases/latest"
	resp, err := c.get(ctx, url)
	if err != nil {
		return Release{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		// A 404 is the ordinary "nothing published yet" case on a fresh fork;
		// spelling it out beats echoing the bare status.
		if resp.StatusCode == http.StatusNotFound {
			return Release{}, fmt.Errorf("no published release for %s/%s", Owner, Repo)
		}
		return Release{}, fmt.Errorf("fetch latest release: %s%s", resp.Status, bodySnippet(resp.Body))
	}

	var rel Release
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxDocBytes)).Decode(&rel); err != nil {
		return Release{}, fmt.Errorf("decode latest release: %w", err)
	}
	if rel.Tag == "" {
		return Release{}, fmt.Errorf("latest release of %s/%s has no tag_name", Owner, Repo)
	}
	return rel, nil
}

// bodySnippet returns a short, single-line excerpt of an error body so a rate
// limit or auth message reaches the user instead of being swallowed.
func bodySnippet(r io.Reader) string {
	b, err := io.ReadAll(io.LimitReader(r, 512))
	if err != nil {
		return ""
	}
	s := strings.Join(strings.Fields(string(b)), " ")
	if s == "" {
		return ""
	}
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return ": " + s
}

// download fetches a whole asset.
func (c *Client) download(ctx context.Context, url string) ([]byte, error) {
	resp, err := c.get(ctx, url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download %s: %s", url, resp.Status)
	}
	// Read one byte past the cap so a truncated-at-the-limit body is reported
	// rather than silently installed.
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxAssetBytes+1))
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", url, err)
	}
	if len(b) > maxAssetBytes {
		return nil, fmt.Errorf("download %s is larger than %d bytes", url, maxAssetBytes)
	}
	return b, nil
}

// fetchChecksum downloads and parses a .sha256 asset.
func (c *Client) fetchChecksum(ctx context.Context, url string) (string, error) {
	resp, err := c.get(ctx, url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download %s: %s", url, resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxDocBytes))
	if err != nil {
		return "", fmt.Errorf("download %s: %w", url, err)
	}
	digest, err := ParseSHA256(string(b))
	if err != nil {
		return "", fmt.Errorf("read checksum %s: %w", url, err)
	}
	return digest, nil
}

// ParseSHA256 extracts the digest from a .sha256 asset.
//
// The releases publish sha256sum output ("<hex>  <name>"), so the file carries
// the asset name as well; a bare digest is accepted too, since that is what other
// tooling writes and the name is redundant next to the asset it sits beside.
func ParseSHA256(s string) (string, error) {
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return "", errors.New("checksum file is empty")
	}
	digest := strings.ToLower(fields[0])
	if len(digest) != sha256.Size*2 {
		return "", fmt.Errorf("checksum %q is not a 64-character hex digest", fields[0])
	}
	if _, err := hex.DecodeString(digest); err != nil {
		return "", fmt.Errorf("checksum %q is not hex: %w", fields[0], err)
	}
	return digest, nil
}

// VerifySHA256 compares downloaded bytes against a digest from ParseSHA256.
func VerifySHA256(data []byte, want string) error {
	got := fmt.Sprintf("%x", sha256.Sum256(data))
	if got != strings.ToLower(want) {
		return fmt.Errorf("checksum mismatch: downloaded %s, expected %s", got, want)
	}
	return nil
}

// version is a parsed release tag: three numeric fields plus whatever followed
// the hyphen. snapshot marks a `git describe` suffix, see isSnapshotSuffix.
type version struct {
	num      [3]uint64
	pre      []string
	snapshot bool
}

// parseVersion accepts v0.1.1, 0.1.1, v0.1 and v1 (missing fields are zero), and
// strips build metadata, which semver excludes from precedence.
func parseVersion(s string) (version, error) {
	var v version
	s = strings.TrimSpace(s)
	if strings.EqualFold(s, DevVersion) {
		return v, ErrDevBuild
	}
	s = strings.TrimPrefix(strings.TrimPrefix(s, "v"), "V")
	if s == "" {
		return v, fmt.Errorf("%w: empty", ErrBadVersion)
	}
	// Build metadata follows a '+', and by definition never affects ordering.
	if i := strings.IndexByte(s, '+'); i >= 0 {
		s = s[:i]
	}
	if i := strings.IndexByte(s, '-'); i >= 0 {
		rest := s[i+1:]
		s = s[:i]
		if isSnapshotSuffix(rest) {
			v.snapshot = true
		} else {
			v.pre = strings.Split(rest, ".")
		}
	}
	parts := strings.Split(s, ".")
	if len(parts) > 3 {
		return v, fmt.Errorf("%w: %q has more than three numeric fields", ErrBadVersion, s)
	}
	for i, p := range parts {
		n, err := strconv.ParseUint(p, 10, 64)
		if err != nil {
			return v, fmt.Errorf("%w: %q", ErrBadVersion, s)
		}
		v.num[i] = n
	}
	return v, nil
}

// isSnapshotSuffix reports whether the text after the first hyphen came from
// `git describe`: "<n>-g<sha>", or the bare "dirty" that --dirty appends to a
// modified working tree.
//
// Such a build is the tag plus commits, or the tag with local edits. Semver
// would read the suffix as a pre-release and sort it BELOW the tag, which would
// make `--update` offer to replace a build from the tip of the branch with the
// older release it descends from — a downgrade dressed up as an update.
// Treating it as the tag itself avoids that; there is no way to order "some
// commits after v0.1.0" against v0.1.1 anyway, so the numeric fields decide.
func isSnapshotSuffix(rest string) bool {
	rest = strings.TrimSuffix(rest, "-dirty")
	if rest == "dirty" {
		return true
	}
	n, sha, ok := strings.Cut(rest, "-")
	if !ok {
		return false
	}
	if _, err := strconv.ParseUint(n, 10, 64); err != nil {
		return false
	}
	if len(sha) < 2 || sha[0] != 'g' {
		return false
	}
	for _, r := range sha[1:] {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

// Compare orders two versions: -1 when a is older, 0 when equal, +1 when newer.
// Prerelease tags follow semver precedence, so v0.2.0-rc.1 is older than v0.2.0.
func Compare(a, b string) (int, error) {
	va, err := parseVersion(a)
	if err != nil {
		return 0, fmt.Errorf("version %q: %w", a, err)
	}
	vb, err := parseVersion(b)
	if err != nil {
		return 0, fmt.Errorf("version %q: %w", b, err)
	}
	for i := range va.num {
		if c := compareUint(va.num[i], vb.num[i]); c != 0 {
			return c, nil
		}
	}
	// Equal numeric fields: one side is a git-describe snapshot of the other's
	// tag, so neither is older.
	if va.snapshot || vb.snapshot {
		return 0, nil
	}
	switch {
	case len(va.pre) == 0 && len(vb.pre) == 0:
		return 0, nil
	case len(va.pre) == 0:
		return 1, nil
	case len(vb.pre) == 0:
		return -1, nil
	}
	return comparePre(va.pre, vb.pre), nil
}

// IsNewer reports whether latest is a newer release than current.
func IsNewer(current, latest string) (bool, error) {
	c, err := Compare(current, latest)
	if err != nil {
		return false, err
	}
	return c < 0, nil
}

func comparePre(a, b []string) int {
	for i := 0; i < len(a) && i < len(b); i++ {
		if c := comparePreIdent(a[i], b[i]); c != 0 {
			return c
		}
	}
	switch {
	case len(a) < len(b):
		return -1
	case len(a) > len(b):
		return 1
	}
	return 0
}

// comparePreIdent applies semver rule 11.4: numeric identifiers compare
// numerically, numeric sorts below alphanumeric, and the rest compares as ASCII.
func comparePreIdent(a, b string) int {
	an, aerr := strconv.ParseUint(a, 10, 64)
	bn, berr := strconv.ParseUint(b, 10, 64)
	switch {
	case aerr == nil && berr == nil:
		return compareUint(an, bn)
	case aerr == nil:
		return -1
	case berr == nil:
		return 1
	}
	return strings.Compare(a, b)
}

func compareUint(a, b uint64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// Supported reports whether the release workflow publishes a binary for a
// platform. Kept in step with PLATFORMS in the Makefile.
func Supported(goos, goarch string) bool {
	if goarch != "amd64" && goarch != "arm64" {
		return false
	}
	switch goos {
	case "linux", "darwin", "windows":
		return true
	}
	return false
}

// AssetName is the published asset for a tag on a platform:
// opencode-limits_<tag>_<goos>_<goarch>[.exe]. Verified against the v0.1.0
// release, which ships exactly these names.
//
// A tag given without its "v" is normalised to the published form; the release
// workflow always tags with one, but a caller reconstructing a name by hand
// should not have to remember that.
func AssetName(tag, goos, goarch string) string {
	tag = "v" + strings.TrimPrefix(strings.TrimPrefix(tag, "v"), "V")
	name := assetPrefix + tag + "_" + goos + "_" + goarch
	if goos == "windows" {
		name += ".exe"
	}
	return name
}

// Installer replaces one file on disk with downloaded bytes.
type Installer struct {
	// Path is the binary to replace. NewInstaller resolves it from the running
	// process.
	Path string
	// GOOS selects the strategy. Empty means the host platform.
	GOOS string
}

// NewInstaller locates the running executable.
func NewInstaller() (Installer, error) {
	p, err := os.Executable()
	if err != nil {
		return Installer{}, fmt.Errorf("locate running executable: %w", err)
	}
	// os.Executable returns the launched path on some platforms. Resolving
	// symlinks means /usr/local/bin/opencode-limits is replaced at its target
	// instead of being clobbered by a regular file, which would silently break
	// the symlink for every other user of the install.
	if resolved, rerr := filepath.EvalSymlinks(p); rerr == nil {
		p = resolved
	}
	return Installer{Path: p, GOOS: runtime.GOOS}, nil
}

func (in Installer) goos() string {
	if in.GOOS == "" {
		return runtime.GOOS
	}
	return in.GOOS
}

// PendingPath is where a staged update waits on Windows: ".new" goes before the
// extension so the file keeps its .exe suffix and stays runnable by hand.
func PendingPath(path string) string {
	ext := filepath.Ext(path)
	return strings.TrimSuffix(path, ext) + ".new" + ext
}

// Install writes data over the target, returning the path written and whether
// the new binary is already in place.
//
// Windows cannot replace a running .exe, so the download is staged beside it and
// the caller tells the user to move it. Elsewhere the replacement is a rename
// within the target's own directory, which is atomic on one filesystem and works
// while the old inode is still executing.
func (in Installer) Install(data []byte) (string, bool, error) {
	if in.Path == "" {
		return "", false, errors.New("installer has no target path")
	}
	dst, applied := in.Path, true
	if in.goos() == "windows" {
		dst, applied = PendingPath(in.Path), false
	}
	if err := writeBinary(dst, data); err != nil {
		return "", false, in.notWritable(data, err)
	}
	return dst, applied, nil
}

// writeBinary writes a temp file in the destination's directory, then renames it
// over the destination. Writing the destination in place would fail with ETXTBSY
// while the old binary runs, and would leave a half-written binary on crash.
func writeBinary(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".new*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	cleanup := func() { _ = os.Remove(name) }
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	// Chmod before the rename so the binary is never in place without its
	// execute bits, not even for an instant.
	if err := os.Chmod(name, 0o755); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(name, path); err != nil {
		cleanup()
		return err
	}
	return nil
}

// NotWritableError reports a binary that could not be replaced, with the path
// and — when one could be saved — a copy of the verified download, so the caller
// can print a command the user can actually run instead of a bare
// "permission denied".
type NotWritableError struct {
	Path    string
	SavedTo string
	Err     error
}

func (e *NotWritableError) Error() string {
	if e.SavedTo != "" {
		return fmt.Sprintf("cannot replace %s (%v).\n"+
			"the verified download is at %s; install it with:\n  sudo install -m 0755 %s %s",
			e.Path, e.Err, e.SavedTo, e.SavedTo, e.Path)
	}
	return fmt.Sprintf("cannot replace %s (%v).\n"+
		"write access to that directory is required; re-run with sudo, or install the release by hand",
		e.Path, e.Err)
}

func (e *NotWritableError) Unwrap() error { return e.Err }

// notWritable wraps a write failure, saving the download when the cause was
// permissions. Other causes (a full disk, a missing directory) would fail the
// same way for the copy, so saving one there would only leave litter behind.
func (in Installer) notWritable(data []byte, err error) error {
	nw := &NotWritableError{Path: in.Path, Err: err}
	if errors.Is(err, fs.ErrPermission) {
		nw.SavedTo = saveCopy(data)
	}
	return nw
}

func saveCopy(data []byte) string {
	f, err := os.CreateTemp("", "opencode-limits-*.new")
	if err != nil {
		return ""
	}
	name := f.Name()
	if _, err := f.Write(data); err != nil {
		f.Close()
		_ = os.Remove(name)
		return ""
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(name)
		return ""
	}
	_ = os.Chmod(name, 0o755)
	return name
}

// Outcome records what Apply did.
type Outcome struct {
	Current string
	// Latest is empty when the check never got a release document.
	Latest string
	// Newer reports that the published release is ahead of Current.
	Newer bool
	// AssetName is the platform's asset in the published release, and AssetURL
	// its download link, so a failed install can still tell the user where to
	// get the file.
	AssetName string
	AssetURL  string
	// Changed is false when the build was already current.
	Changed bool
	// Applied is true when the new binary is now in place; Path names it.
	Applied bool
	Path    string
	// Target is the binary the update was aimed at, which differs from Path on
	// Windows where the download is only staged.
	Target string
	// Pending is the staged file when the running binary could not be replaced.
	Pending string
}

// Check reports whether a newer release exists, without downloading anything.
func (c *Client) Check(ctx context.Context, current string) (Outcome, error) {
	out, _, err := c.resolve(ctx, current)
	return out, err
}

// resolve fetches the newest release and decides whether it is ahead of
// current, returning both the outcome and the release so Apply does not have to
// ask GitHub the same question twice.
func (c *Client) resolve(ctx context.Context, current string) (Outcome, Release, error) {
	out := Outcome{Current: current}
	if strings.EqualFold(strings.TrimSpace(current), DevVersion) {
		return out, Release{}, ErrDevBuild
	}
	rel, err := c.Latest(ctx)
	if err != nil {
		return out, Release{}, err
	}
	out.Latest = rel.Tag

	newer, err := IsNewer(current, rel.Tag)
	if err != nil {
		return out, rel, fmt.Errorf("compare %q with %q: %w", current, rel.Tag, err)
	}
	out.Newer = newer
	if !newer {
		return out, rel, nil
	}

	goos, goarch := c.goos(), c.goarch()
	if !Supported(goos, goarch) {
		return out, rel, fmt.Errorf("release %s publishes no binary for %s/%s; see %s",
			rel.Tag, goos, goarch, rel.HTMLURL)
	}
	out.AssetName = AssetName(rel.Tag, goos, goarch)
	if asset, ok := rel.Asset(out.AssetName); ok {
		out.AssetURL = asset.URL
	}
	return out, rel, nil
}

// Apply checks for a newer release and, when one exists, verifies and installs
// it. Nothing is written unless the checksum matches: a binary that cannot be
// verified is not installed at all.
func (c *Client) Apply(ctx context.Context, current string, in Installer) (Outcome, error) {
	out, rel, err := c.resolve(ctx, current)
	if err != nil || !out.Newer {
		return out, err
	}

	asset, ok := rel.Asset(out.AssetName)
	if !ok {
		return out, fmt.Errorf("release %s has no asset %s (published: %s)",
			rel.Tag, out.AssetName, strings.Join(rel.assetNames(), ", "))
	}
	out.AssetURL = asset.URL

	shaAsset, ok := rel.Asset(out.AssetName + ".sha256")
	if !ok {
		// Without a published digest the download cannot be verified, and an
		// unverified download is exactly what this check exists to prevent.
		return out, fmt.Errorf("release %s publishes no %s.sha256, so the download cannot be verified; install it by hand from %s",
			rel.Tag, out.AssetName, asset.URL)
	}
	// The checksum first: it is a few bytes, and a missing or unreadable one
	// should not cost a wasted binary download.
	want, err := c.fetchChecksum(ctx, shaAsset.URL)
	if err != nil {
		return out, err
	}
	data, err := c.download(ctx, asset.URL)
	if err != nil {
		return out, err
	}
	if err := VerifySHA256(data, want); err != nil {
		return out, fmt.Errorf("verify %s: %w", out.AssetName, err)
	}

	out.Changed = true
	out.Target = in.Path
	path, applied, err := in.Install(data)
	if err != nil {
		return out, err
	}
	if applied {
		out.Applied, out.Path = true, path
		return out, nil
	}
	out.Pending = path
	return out, nil
}
