package main

import "testing"

// releaseVersionFromModule gates which module versions may stand in for a
// -ldflags stamp. The stakes are real: whatever it returns becomes the version
// the self-updater compares against the newest GitHub release.
func TestReleaseVersionFromModule(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
		why  string
	}{
		{
			name: "proxy build carries the tag verbatim",
			in:   "v0.1.2",
			want: "v0.1.2",
			why:  "this is the case the fallback exists for: go install cannot pass -ldflags",
		},
		{
			name: "working tree build is a pseudo-version",
			in:   "v0.1.3-0.20261119113358-d31140ff1d41",
			want: "",
			why:  "names commits after v0.1.3; comparing it would rank a stale checkout below the release it descends from",
		},
		{
			name: "dirty working tree",
			in:   "v0.1.3-0.20261119113358-d31140ff1d41+dirty",
			want: "",
			why:  "same, and the local edits are not in any release",
		},
		{
			name: "devel sentinel",
			in:   "(devel)",
			want: "",
			why:  "a build with no version information at all",
		},
		{
			name: "empty",
			in:   "",
			want: "",
			why:  "no build info available",
		},
		{
			name: "genuine prerelease is not a pseudo-version",
			in:   "v0.2.0-rc.1",
			want: "v0.2.0-rc.1",
			why:  "a real tag; the update comparator handles semver precedence",
		},
		{
			name: "no v prefix",
			in:   "0.1.2",
			want: "",
			why:  "go module versions are always v-prefixed; anything else is not a release tag",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := releaseVersionFromModule(tt.in); got != tt.want {
				t.Errorf("releaseVersionFromModule(%q) = %q, want %q\n  %s", tt.in, got, tt.want, tt.why)
			}
		})
	}
}

// TestUnstampedBuildStillReportsDev guards the invariant that a local build is
// never mistaken for a release. If this ever fails, a working-tree binary would
// start offering to replace itself with a download.
func TestUnstampedBuildStillReportsDev(t *testing.T) {
	// `go test -ldflags` is possible, and CI pins no version, but do not couple the
	// suite to how it was invoked: a stamped test binary is a valid way to run it.
	if version != "dev" {
		t.Skipf("test binary was built with version %q, not an unstamped dev build", version)
	}
	// Resolve again to confirm a second call is idempotent and does not invent a
	// version from a devel-only build.
	resolveVersion()
	if version != "dev" {
		t.Errorf("resolveVersion changed an unstamped build to %q", version)
	}
}
